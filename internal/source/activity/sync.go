package activity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	sdkactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"sentinel/internal/payload"
	"sentinel/internal/source"
)

const leaseDuration = time.Minute

type Activities struct {
	Store    source.SyncStore
	Reads    source.ReadStore
	Payloads payload.Store
	Archives source.ArchiveStore
	Git      source.Git
}

func putJSON(ctx context.Context, store payload.Store, tenant, kind string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return store.Put(ctx, tenant, kind, data)
}

func (a *Activities) Create(ctx context.Context, repo source.Repository) (source.SyncJob, error) {
	if err := source.ValidateRepository(repo); err != nil {
		return source.SyncJob{}, err
	}
	if repo.Status != "active" {
		return source.SyncJob{}, source.ErrTerminal
	}
	ref, err := putJSON(ctx, a.Payloads, repo.TenantID, "source-config", repo)
	if err != nil {
		return source.SyncJob{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return source.SyncJob{}, err
	}
	request := source.SyncRequest{TenantID: repo.TenantID, RepositoryID: repo.ID, SyncID: id.String(), OperationID: source.OperationID(repo.ID, id.String())}
	return a.Store.CreateSync(ctx, source.SyncJob{Request: request, ConfigRef: ref, WorkflowID: source.WorkflowID(repo.TenantID, id.String())})
}

func (a *Activities) candidate(ctx context.Context, request source.SyncRequest, ref string) (source.Candidate, error) {
	var c source.Candidate
	data, err := a.Payloads.Get(ctx, request.TenantID, ref)
	if err == nil {
		err = json.Unmarshal(data, &c)
	}
	if err == nil && (c.Version != 1 || c.Request != request) {
		err = errors.New("source result identity mismatch")
	}
	return c, err
}

func resultOf(c source.Candidate, ref string) source.SyncResult {
	return source.SyncResult{RepositoryID: c.Request.RepositoryID, SyncID: c.Request.SyncID,
		ManifestID: c.Manifest.ID, ResultRef: ref, SnapshotCount: len(c.Snapshots)}
}

// Run adds Temporal heartbeats; Sync also works without a Temporal Activity
// context, allowing the complete importer to be integration-tested directly.
func (a *Activities) Run(ctx context.Context, request source.SyncRequest) (source.SyncResult, error) {
	if !source.ValidID(request.TenantID) || !source.ValidID(request.RepositoryID) || !source.ValidID(request.SyncID) ||
		request.OperationID != source.OperationID(request.RepositoryID, request.SyncID) {
		return source.SyncResult{}, temporal.NewNonRetryableApplicationError("invalid source sync identity", "source_invalid", nil)
	}
	heartbeatCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				sdkactivity.RecordHeartbeat(ctx, request.SyncID)
			}
		}
	}()
	result, err := a.Sync(ctx, request)
	if errors.Is(err, source.ErrTerminal) || errors.Is(err, source.ErrNotFound) {
		return result, temporal.NewNonRetryableApplicationError(err.Error(), "source_terminal", err)
	}
	return result, err
}

func (a *Activities) Sync(ctx context.Context, request source.SyncRequest) (source.SyncResult, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return source.SyncResult{}, err
	}
	job, repo, err := a.Store.ClaimSync(ctx, request, id.String(), leaseDuration)
	if err != nil {
		return source.SyncResult{}, err
	}
	if job.Status == "succeeded" {
		c, err := a.candidate(ctx, request, job.ResultRef)
		return resultOf(c, job.ResultRef), err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	renewed := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				renewed <- nil
				return
			case <-ticker.C:
				checkCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				err := a.Store.RenewSync(checkCtx, job, leaseDuration)
				stop()
				if err != nil {
					cancel()
					renewed <- err
					return
				}
			}
		}
	}()
	result, err := a.synchronize(ctx, job, repo)
	cancel()
	if leaseErr := <-renewed; leaseErr != nil && err != nil {
		return result, leaseErr
	}
	return result, err
}

func (a *Activities) synchronize(ctx context.Context, job source.SyncJob, repo source.Repository) (source.SyncResult, error) {
	if job.ResultRef != "" {
		candidate, err := a.candidate(ctx, job.Request, job.ResultRef)
		if err != nil {
			return source.SyncResult{}, err
		}
		if source.ValidateCandidate(candidate, repo) == nil {
			return a.Store.Publish(ctx, job, candidate, job.ResultRef)
		}
	}
	data, err := a.Payloads.Get(ctx, repo.TenantID, job.ConfigRef)
	if err != nil {
		return source.SyncResult{}, err
	}
	var config source.Repository
	if err := json.Unmarshal(data, &config); err != nil {
		return source.SyncResult{}, err
	}
	if err := source.ValidateRepository(config); err != nil {
		return source.SyncResult{}, err
	}
	if config.ID != repo.ID || config.TenantID != repo.TenantID || config.RemoteURL != repo.RemoteURL ||
		config.CredentialRef != repo.CredentialRef || config.ObjectFormat != repo.ObjectFormat || !slices.Equal(config.TrackedRefs, repo.TrackedRefs) {
		return source.SyncResult{}, errors.New("repository configuration changed; create a new sync")
	}
	token := ""
	if config.CredentialRef != "" {
		token = os.Getenv(strings.TrimPrefix(config.CredentialRef, "env:"))
		if token == "" || strings.ContainsAny(token, "\x00\r\n") {
			return source.SyncResult{}, errors.New("source credential reference is unavailable or invalid")
		}
	}
	workspace, err := a.Git.Open(ctx, repo.TenantID, repo.ID, repo.ObjectFormat)
	if err != nil {
		return source.SyncResult{}, err
	}
	defer workspace.Close()
	previous, err := a.restore(ctx, workspace, repo)
	if err != nil {
		return source.SyncResult{}, err
	}
	refs, err := workspace.Fetch(ctx, config.RemoteURL, config.TrackedRefs, token)
	if err != nil {
		return source.SyncResult{}, err
	}
	if err := workspace.Verify(ctx); err != nil {
		return source.SyncResult{}, err
	}
	state, err := workspace.State(ctx)
	if err != nil {
		return source.SyncResult{}, err
	}
	candidate := source.Candidate{Version: 1, Request: job.Request, ExpectedArchiveVersion: repo.ArchiveVersion,
		ParentManifestID: repo.ArchiveHeadID, ArchiveAttempt: job.Fence, Refs: refs,
		Manifest: source.Manifest{ID: repo.ArchiveHeadID}}
	bundlePath := filepath.Join(workspace.Directory(), "output.bundle")
	bundle, err := workspace.Bundle(ctx, bundlePath, previous)
	if err != nil {
		return source.SyncResult{}, err
	}
	if !bundle.Empty {
		object, err := a.Archives.Put(ctx, repo.TenantID, repo.ID, bundlePath)
		if err != nil {
			return source.SyncResult{}, err
		}
		manifestID, err := uuid.NewV7()
		if err != nil {
			return source.SyncResult{}, err
		}
		kind := source.BundleBase
		if repo.ArchiveHeadID != "" {
			kind = source.BundleIncrement
		}
		candidate.Manifest = source.Manifest{ID: manifestID.String(), RepositoryID: repo.ID, ParentManifestID: repo.ArchiveHeadID,
			BundleKind: kind, BundleRef: object.Ref, BundleChecksum: object.Checksum,
			PrerequisiteCommitSHAs: bundle.Prerequisites, OperationID: job.Request.OperationID, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
		candidate.ManifestRef, err = putJSON(ctx, a.Payloads, repo.TenantID, "git-manifest", source.ArchiveDocument{
			Version: 1, TenantID: repo.TenantID, Manifest: candidate.Manifest, ObjectFormat: repo.ObjectFormat, Roots: state.Roots})
		if err != nil {
			return source.SyncResult{}, err
		}
		candidate.NewArchive = true
	}
	known, err := a.Store.KnownCommits(ctx, repo.TenantID, repo.ID)
	if err != nil {
		return source.SyncResult{}, err
	}
	for _, commit := range state.Commits {
		old, exists := known[commit.SHA]
		if exists && old.TreeSHA != commit.TreeSHA {
			return source.SyncResult{}, errors.New("immutable commit tree mismatch")
		}
		if exists && old.Status == source.SnapshotReady {
			continue
		}
		snapshotID := old.ID
		createdAt := old.CreatedAt
		if !exists {
			id, err := uuid.NewV7()
			if err != nil {
				return source.SyncResult{}, err
			}
			snapshotID = id.String()
			createdAt = time.Now().UTC().Truncate(time.Microsecond)
		}
		candidate.Snapshots = append(candidate.Snapshots, source.Snapshot{ID: snapshotID, RepositoryID: repo.ID,
			CommitSHA: commit.SHA, TreeSHA: commit.TreeSHA, StorageManifestID: candidate.Manifest.ID, Status: source.SnapshotReady, CreatedAt: createdAt})
	}
	if err := source.ValidateCandidate(candidate, repo); err != nil {
		return source.SyncResult{}, err
	}
	ref, err := putJSON(ctx, a.Payloads, repo.TenantID, "source-result", candidate)
	if err != nil {
		return source.SyncResult{}, err
	}
	if err := a.Store.SaveCandidate(ctx, job, candidate, ref); err != nil {
		return source.SyncResult{}, err
	}
	return a.Store.Publish(ctx, job, candidate, ref)
}

func (a *Activities) restore(ctx context.Context, workspace source.Workspace, repo source.Repository) ([]string, error) {
	chain := make([]source.ArchiveDocument, 0)
	seen := make(map[string]bool)
	for id := repo.ArchiveHeadID; id != ""; {
		if seen[id] || len(chain) >= 10000 {
			return nil, errors.New("cyclic or oversized archive chain")
		}
		seen[id] = true
		record, err := a.Store.LoadManifest(ctx, repo.TenantID, repo.ID, id)
		if err != nil {
			return nil, err
		}
		data, err := a.Payloads.Get(ctx, repo.TenantID, record.ObjectRef)
		if err != nil {
			return nil, err
		}
		var document source.ArchiveDocument
		if err := json.Unmarshal(data, &document); err != nil {
			return nil, err
		}
		m, indexed := document.Manifest, record.Manifest
		if document.Version != 1 || document.TenantID != repo.TenantID || document.ObjectFormat != repo.ObjectFormat ||
			m.ID != id || m.RepositoryID != repo.ID || m.ParentManifestID != indexed.ParentManifestID || m.BundleKind != indexed.BundleKind ||
			m.BundleRef != indexed.BundleRef || m.BundleChecksum != indexed.BundleChecksum ||
			!slices.Equal(m.PrerequisiteCommitSHAs, indexed.PrerequisiteCommitSHAs) ||
			(m.ParentManifestID == "" && m.BundleKind != source.BundleBase) || (m.ParentManifestID != "" && m.BundleKind != source.BundleIncrement) {
			return nil, errors.New("archive manifest integrity mismatch")
		}
		chain = append(chain, document)
		id = m.ParentManifestID
	}
	for i := len(chain) - 1; i >= 0; i-- {
		path := filepath.Join(workspace.Directory(), fmt.Sprintf("restore-%d.bundle", i))
		if err := a.Archives.Get(ctx, repo.TenantID, chain[i].Manifest.BundleRef, path); err != nil {
			return nil, err
		}
		if err := workspace.Import(ctx, path); err != nil {
			return nil, err
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	if err := workspace.Verify(ctx); err != nil {
		return nil, err
	}
	state, err := workspace.State(ctx)
	if err != nil {
		return nil, err
	}
	if len(chain) > 0 && !slices.Equal(state.Roots, source.SortedRefs(chain[0].Roots)) {
		return nil, errors.New("restored archive roots mismatch")
	}
	return state.Roots, nil
}

func (a *Activities) Finish(ctx context.Context, input source.FinishInput) error {
	ref, err := putJSON(ctx, a.Payloads, input.Request.TenantID, "source-error", map[string]string{"status": input.Status})
	if err != nil {
		return err
	}
	return a.Store.FinishSync(ctx, input.Request, input.Status, ref)
}
