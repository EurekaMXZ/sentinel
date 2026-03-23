package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"time"

	"github.com/google/uuid"
	enums "go.temporal.io/api/enums/v1"
	sdkactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	sdkworkflow "go.temporal.io/sdk/workflow"

	"sentinel/internal/platform/config"
	"sentinel/internal/platform/temporal"
	"sentinel/internal/source"
	"sentinel/internal/source/activity"
	sourcegit "sentinel/internal/source/git"
	sourceworkflow "sentinel/internal/source/workflow"
	"sentinel/internal/storage/postgres"
	"sentinel/internal/storage/s3"
)

func openSourceRuntime(ctx context.Context, cfg config.Config) (*activity.Activities, func(), error) {
	binary, err := exec.LookPath(cfg.Source.GitBinary)
	if err != nil {
		return nil, nil, fmt.Errorf("find Git executable: %w", err)
	}
	objectClient, err := s3.New(cfg.S3)
	if err != nil {
		return nil, nil, err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	exists, err := objectClient.BucketExists(checkCtx, cfg.S3.Bucket)
	if err != nil {
		return nil, nil, fmt.Errorf("check S3 bucket: %w", err)
	}
	if !exists {
		return nil, nil, errors.New("S3 bucket is missing; run minio-init first")
	}
	pool, err := postgres.Open(ctx, cfg.Postgres)
	if err != nil {
		return nil, nil, err
	}
	index := postgres.NewPayloadIndex(pool)
	store := postgres.NewSourceStore(pool)
	return &activity.Activities{Store: store, Reads: store,
		Payloads: s3.NewPayloadStore(objectClient, cfg.S3.Bucket, index),
		Archives: s3.NewArchiveStore(objectClient, cfg.S3.Bucket, index, cfg.Source.MaxBundleBytes),
		Git:      sourcegit.Client{Binary: binary, CacheDir: cfg.Source.CacheDir}}, pool.Close, nil
}

func registerSource(registry worker.Registry, activities *activity.Activities) {
	registry.RegisterWorkflowWithOptions(sourceworkflow.SourceSyncWorkflow, sdkworkflow.RegisterOptions{Name: source.WorkflowName})
	registry.RegisterActivityWithOptions(activities.Run, sdkactivity.RegisterOptions{Name: source.SyncActivity})
	registry.RegisterActivityWithOptions(activities.Read, sdkactivity.RegisterOptions{Name: source.ReadActivity})
	registry.RegisterActivityWithOptions(activities.Finish, sdkactivity.RegisterOptions{Name: source.FinishActivity})
}

type StartSourceOptions struct {
	TenantID      string
	URL           string
	RepositoryID  string
	ResumeID      string
	CredentialRef string
	TrackedRefs   []string
	Wait          bool
	Output        io.Writer
}

// StartSourceSync is an administrative entrypoint. A future HTTP adapter must
// obtain the tenant from authentication and authorize the repository separately.
func StartSourceSync(ctx context.Context, cfg config.Config, logger *slog.Logger, options StartSourceOptions) error {
	tenant, err := uuid.Parse(options.TenantID)
	if err != nil || tenant == uuid.Nil || options.Output == nil {
		return errors.New("a nonzero tenant UUID and output writer are required")
	}
	options.TenantID = tenant.String()
	modes := 0
	for _, value := range []string{options.URL, options.RepositoryID, options.ResumeID} {
		if value != "" {
			modes++
		}
	}
	if modes != 1 || (options.URL == "" && (options.CredentialRef != "" || len(options.TrackedRefs) != 0)) {
		return errors.New("choose exactly one of URL, repository, or resume; new configuration requires URL")
	}
	if options.RepositoryID != "" && !source.ValidID(options.RepositoryID) || options.ResumeID != "" && !source.ValidID(options.ResumeID) {
		return errors.New("repository and resume must be nonzero UUIDs")
	}
	var repo source.Repository
	if options.URL != "" {
		remote, err := source.NormalizeGitHubURL(options.URL)
		if err != nil {
			return err
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		refs := options.TrackedRefs
		if len(refs) == 0 {
			refs = []string{"refs/heads/*", "refs/tags/*"}
		}
		repo = source.Repository{ID: id.String(), TenantID: options.TenantID, RemoteURL: remote,
			CredentialRef: options.CredentialRef, TrackedRefs: source.SortedRefs(refs), ObjectFormat: "sha1", Status: "active"}
		if err := source.ValidateRepository(repo); err != nil {
			return err
		}
	}
	activities, closeRuntime, err := openSourceRuntime(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeRuntime()
	var job source.SyncJob
	if options.ResumeID != "" {
		job, err = activities.Store.LoadSync(ctx, source.SyncRequest{TenantID: options.TenantID, SyncID: options.ResumeID})
	} else {
		if options.URL != "" {
			repo, err = activities.Store.CreateRepository(ctx, repo)
		} else {
			repo, err = activities.Store.LoadRepository(ctx, options.TenantID, options.RepositoryID)
		}
		if err == nil {
			job, err = activities.Create(ctx, repo)
		}
	}
	if err != nil {
		return err
	}
	if job.Status == "failed" || job.Status == "cancelled" {
		return errors.New("sync is terminal; start a new sync with -repository")
	}
	encoder := json.NewEncoder(options.Output)
	if err := encoder.Encode(map[string]string{"tenant_id": job.Request.TenantID, "repository_id": job.Request.RepositoryID,
		"sync_id": job.Request.SyncID, "workflow_id": job.WorkflowID}); err != nil {
		return err
	}
	result := source.SyncResult{RepositoryID: job.Request.RepositoryID, SyncID: job.Request.SyncID, ResultRef: job.ResultRef}
	if job.Status != "succeeded" {
		c, err := temporal.Dial(ctx, cfg.Temporal, logger)
		if err != nil {
			return err
		}
		defer c.Close()
		run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: job.WorkflowID,
			TaskQueue: cfg.Temporal.SourceTaskQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
			WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING}, source.WorkflowName, source.WorkflowInput{Request: job.Request})
		if err != nil {
			return fmt.Errorf("start Source Workflow (resume with the saved sync ID): %w", err)
		}
		if !options.Wait {
			return nil
		}
		if err := run.Get(ctx, &result); err != nil {
			return err
		}
	}
	if !options.Wait {
		return nil
	}
	data, err := activities.Payloads.Get(ctx, options.TenantID, result.ResultRef)
	if err != nil {
		return err
	}
	var body source.Candidate
	if err := json.Unmarshal(data, &body); err != nil {
		return err
	}
	if body.Version != 1 || body.Request != job.Request {
		return errors.New("invalid sync result")
	}
	return encoder.Encode(struct {
		source.SyncResult
		Snapshots []source.Snapshot `json:"snapshots"`
	}{source.SyncResult{RepositoryID: body.Request.RepositoryID, SyncID: body.Request.SyncID,
		ManifestID: body.Manifest.ID, ResultRef: result.ResultRef, SnapshotCount: len(body.Snapshots)}, body.Snapshots})
}
