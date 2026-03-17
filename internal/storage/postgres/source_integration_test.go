//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"sentinel/internal/source"
	sourceactivity "sentinel/internal/source/activity"
	sourcegit "sentinel/internal/source/git"
	"sentinel/internal/storage/postgres"
	"sentinel/internal/storage/s3"
)

type localSourceGit struct {
	source.Git
	remote   string
	disabled bool
}

type localSourceWorkspace struct {
	source.Workspace
	remote string
}

func (g *localSourceGit) Open(ctx context.Context, tenant, repo, format string) (source.Workspace, error) {
	if g.disabled {
		return nil, errors.New("Git intentionally unavailable")
	}
	w, err := g.Git.Open(ctx, tenant, repo, format)
	return localSourceWorkspace{w, g.remote}, err
}

func (w localSourceWorkspace) Fetch(ctx context.Context, _ string, refs []string, token string) ([]source.RefObservation, error) {
	return w.Workspace.Fetch(ctx, w.remote, refs, token)
}

type sourceFixture struct {
	pool    *pgxpool.Pool
	store   *postgres.SourceStore
	a       *sourceactivity.Activities
	git     *localSourceGit
	repo    source.Repository
	mu      sync.Mutex
	objects map[string][]byte
}

func sourceCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func (f *sourceFixture) commit(t *testing.T, body string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.git.remote, "source.txt"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	sourceCommand(t, f.git.remote, "add", "source.txt")
	sourceCommand(t, f.git.remote, "commit", "-m", body)
	return sourceCommand(t, f.git.remote, "rev-parse", "HEAD")
}

func newSourceFixture(t *testing.T) *sourceFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is required")
	}
	dsn := isolatedDatabase(t)
	provider := openProvider(t, t.Context(), dsn)
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := &sourceFixture{pool: pool, store: postgres.NewSourceStore(pool), objects: make(map[string][]byte)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("unsigned S3 request")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			f.objects[r.URL.Path] = data
			w.Header().Set("ETag", `"test-etag"`)
		case http.MethodGet, http.MethodHead:
			data, ok := f.objects[r.URL.Path]
			if !ok {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			w.Header().Set("ETag", `"test-etag"`)
			w.Header().Set("Last-Modified", time.Unix(0, 0).UTC().Format(http.TimeFormat))
			if r.Method == http.MethodGet {
				_, _ = w.Write(data)
			}
		default:
			t.Errorf("unexpected S3 method %s", r.Method)
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(server.Close)
	client, err := minio.New(strings.TrimPrefix(server.URL, "https://"), &minio.Options{
		Creds: credentials.NewStaticV4("test-access", "test-secret", ""), Secure: true,
		Region: "us-east-1", Transport: server.Client().Transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	index := postgres.NewPayloadIndex(pool)
	f.git = &localSourceGit{Git: sourcegit.Client{Binary: "git", CacheDir: t.TempDir()}, remote: t.TempDir()}
	sourceCommand(t, f.git.remote, "init", "-b", "main")
	sourceCommand(t, f.git.remote, "config", "user.name", "Source Test")
	sourceCommand(t, f.git.remote, "config", "user.email", "source@example.test")
	f.a = &sourceactivity.Activities{Store: f.store, Git: f.git,
		Payloads: s3.NewPayloadStore(client, "source-test", index), Archives: s3.NewArchiveStore(client, "source-test", index, 32<<20)}
	f.repo, err = f.store.CreateRepository(t.Context(), source.Repository{ID: newID(t), TenantID: newID(t),
		RemoteURL: "https://github.com/owner/repository.git", TrackedRefs: []string{"refs/heads/*", "refs/tags/*"}, ObjectFormat: "sha1"})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *sourceFixture) job(t *testing.T) source.SyncJob {
	t.Helper()
	repo, err := f.store.LoadRepository(t.Context(), f.repo.TenantID, f.repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	j, err := f.a.Create(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func (f *sourceFixture) synchronize(t *testing.T) source.SyncResult {
	t.Helper()
	j := f.job(t)
	r, err := f.a.Sync(t.Context(), j.Request)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSourceImportIncrementRetentionAndTenantIsolation(t *testing.T) {
	f := newSourceFixture(t)
	first := f.commit(t, "first")
	sourceCommand(t, f.git.remote, "tag", "-a", "v1", "-m", "version one")
	sourceCommand(t, f.git.remote, "branch", "obsolete")
	base := f.synchronize(t)
	if base.SnapshotCount != 1 || base.ManifestID == "" {
		t.Fatalf("base=%+v", base)
	}
	f.git.disabled = true
	job, err := f.store.LoadSync(t.Context(), source.SyncRequest{TenantID: f.repo.TenantID, SyncID: base.SyncID})
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := f.a.Sync(t.Context(), job.Request); err != nil || replay != base {
		t.Fatalf("completed replay=%+v err=%v", replay, err)
	}
	f.git.disabled = false
	noop := f.synchronize(t)
	if noop.ManifestID != base.ManifestID || noop.SnapshotCount != 0 {
		t.Fatalf("unchanged=%+v", noop)
	}
	second := f.commit(t, "second")
	delta := f.synchronize(t)
	manifest, err := f.store.LoadManifest(t.Context(), f.repo.TenantID, f.repo.ID, delta.ManifestID)
	if err != nil || manifest.Manifest.BundleKind != source.BundleIncrement || manifest.Manifest.ParentManifestID != base.ManifestID {
		t.Fatalf("delta manifest=%+v err=%v", manifest, err)
	}
	sourceCommand(t, f.git.remote, "tag", "-d", "v1")
	sourceCommand(t, f.git.remote, "branch", "-D", "obsolete")
	sourceCommand(t, f.git.remote, "checkout", "--orphan", "rewrite")
	sourceCommand(t, f.git.remote, "rm", "-rf", ".")
	third := f.commit(t, "rewritten")
	sourceCommand(t, f.git.remote, "branch", "-M", "main")
	forced := f.synchronize(t)
	if forced.SnapshotCount != 1 {
		t.Fatalf("force-push=%+v", forced)
	}
	known, err := f.store.KnownCommits(t.Context(), f.repo.TenantID, f.repo.ID)
	if err != nil || len(known) != 3 {
		t.Fatalf("known=%+v err=%v", known, err)
	}
	for _, sha := range []string{first, second, third} {
		if known[sha].Status != source.SnapshotReady || known[sha].CreatedAt.IsZero() {
			t.Fatalf("snapshot not ready: %+v", known[sha])
		}
	}
	// Every attempt starts with an empty cache; this also verifies restoration of
	// the complete durable chain after upstream history has been rewritten.
	if again := f.synchronize(t); again.SnapshotCount != 0 || again.ManifestID != forced.ManifestID {
		t.Fatalf("restored sync=%+v", again)
	}
	var version int64
	var deleted, outbox int
	if err := f.pool.QueryRow(t.Context(), `SELECT archive_version FROM source_repositories WHERE repository_id=$1`, f.repo.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM source_ref_observations WHERE sync_id=$1 AND is_deleted`, forced.SyncID).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox WHERE tenant_id=$1`, f.repo.TenantID).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if version != 3 || deleted != 2 || outbox != 5 {
		t.Fatalf("version=%d deleted=%d outbox=%d", version, deleted, outbox)
	}
	if _, err := f.store.LoadSync(t.Context(), source.SyncRequest{TenantID: newID(t), SyncID: base.SyncID}); !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("cross-tenant sync=%v", err)
	}
	if _, err := f.a.Payloads.Get(t.Context(), newID(t), base.ResultRef); err == nil {
		t.Fatal("cross-tenant result read succeeded")
	}
	if _, err := f.store.LoadManifest(t.Context(), f.repo.TenantID, newID(t), base.ManifestID); !errors.Is(err, source.ErrNotFound) {
		t.Fatalf("cross-repository archive=%v", err)
	}
}

type publicationFailure struct{ source.SyncStore }

func (s publicationFailure) Publish(context.Context, source.SyncJob, source.Candidate, string) (source.SyncResult, error) {
	return source.SyncResult{}, errors.New("publication response lost")
}

func TestSourceSavedCandidateSurvivesRetryWithoutGit(t *testing.T) {
	f := newSourceFixture(t)
	f.commit(t, "candidate")
	job := f.job(t)
	f.a.Store = publicationFailure{f.store}
	if _, err := f.a.Sync(t.Context(), job.Request); err == nil {
		t.Fatal("expected injected publication failure")
	}
	saved, err := f.store.LoadSync(t.Context(), job.Request)
	if err != nil || saved.ResultRef == "" || saved.Status != "publishing" {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	_, err = f.pool.Exec(t.Context(), `UPDATE idempotency_records SET lease_until=clock_timestamp()-interval '1 second' WHERE tenant_id=$1 AND operation_id=$2`, job.Request.TenantID, job.Request.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	f.a.Store = f.store
	f.git.disabled = true
	result, err := f.a.Sync(t.Context(), job.Request)
	if err != nil || result.ResultRef != saved.ResultRef || result.SnapshotCount != 1 {
		t.Fatalf("retry=%+v err=%v", result, err)
	}
}

func TestSourceLeaseFencesExpiredWorkers(t *testing.T) {
	f := newSourceFixture(t)
	first, second := f.job(t), f.job(t)
	old, _, err := f.store.ClaimSync(t.Context(), first.Request, "old-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.ClaimSync(t.Context(), second.Request, "new-worker", time.Minute); !errors.Is(err, source.ErrBusy) {
		t.Fatalf("concurrent claim=%v", err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE idempotency_records SET lease_until=clock_timestamp()-interval '1 second' WHERE tenant_id=$1 AND operation_id=$2`, old.Request.TenantID, old.Request.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.ClaimSync(t.Context(), second.Request, "new-worker", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RenewSync(t.Context(), old, time.Minute); !errors.Is(err, source.ErrLeaseLost) {
		t.Fatalf("stale renewal=%v", err)
	}
	if _, err := f.store.Publish(t.Context(), old, source.Candidate{}, ""); !errors.Is(err, source.ErrLeaseLost) {
		t.Fatalf("stale publish=%v", err)
	}
	if err := f.store.FinishSync(t.Context(), second.Request, "cancelled", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.ClaimSync(t.Context(), first.Request, "recovered-worker", time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestSourceCorruptArchiveCannotPublishSnapshots(t *testing.T) {
	f := newSourceFixture(t)
	f.commit(t, "durable")
	f.synchronize(t)
	f.commit(t, "unpublished")
	f.mu.Lock()
	for key, data := range f.objects {
		if strings.HasSuffix(key, ".bundle") {
			corrupted := append([]byte(nil), data...)
			corrupted[len(corrupted)-1] ^= 1
			f.objects[key] = corrupted
		}
	}
	f.mu.Unlock()
	job := f.job(t)
	if _, err := f.a.Sync(t.Context(), job.Request); err == nil {
		t.Fatal("corrupt archive accepted")
	}
	known, err := f.store.KnownCommits(t.Context(), f.repo.TenantID, f.repo.ID)
	if err != nil || len(known) != 1 {
		t.Fatalf("published after corruption=%+v err=%v", known, err)
	}
}

func expireSourceLease(t *testing.T, f *sourceFixture, job source.SyncJob) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `UPDATE idempotency_records SET lease_until=clock_timestamp()-interval '1 second'
		WHERE tenant_id=$1 AND operation_id=$2`, job.Request.TenantID, job.Request.OperationID); err != nil {
		t.Fatal(err)
	}
}

func TestSourceCandidateRebuildsAfterArchiveHeadAdvances(t *testing.T) {
	f := newSourceFixture(t)
	f.commit(t, "base")
	f.synchronize(t)
	f.commit(t, "saved candidate")
	job := f.job(t)
	f.a.Store = publicationFailure{f.store}
	if _, err := f.a.Sync(t.Context(), job.Request); err == nil {
		t.Fatal("expected publication failure")
	}
	saved, err := f.store.LoadSync(t.Context(), job.Request)
	if err != nil {
		t.Fatal(err)
	}
	expireSourceLease(t, f, job)
	f.a.Store = f.store
	f.commit(t, "advanced upstream")
	advanced := f.synchronize(t)
	if advanced.SnapshotCount != 2 {
		t.Fatalf("advanced=%+v", advanced)
	}
	rebuilt, err := f.a.Sync(t.Context(), job.Request)
	if err != nil || rebuilt.ManifestID != advanced.ManifestID || rebuilt.SnapshotCount != 0 || rebuilt.ResultRef == saved.ResultRef {
		t.Fatalf("rebuilt=%+v err=%v", rebuilt, err)
	}
	var version int64
	if err := f.pool.QueryRow(t.Context(), `SELECT archive_version FROM source_repositories WHERE repository_id=$1`, f.repo.ID).Scan(&version); err != nil || version != 2 {
		t.Fatalf("archive version=%d err=%v", version, err)
	}
}

func TestSourcePublicationRollsBackAndReusesCandidate(t *testing.T) {
	f := newSourceFixture(t)
	f.commit(t, "atomic publication")
	if _, err := f.pool.Exec(t.Context(), `CREATE FUNCTION reject_source_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected source snapshot failure'; END $$;
		CREATE TRIGGER reject_source_snapshot BEFORE INSERT ON source_snapshots FOR EACH ROW EXECUTE FUNCTION reject_source_snapshot()`); err != nil {
		t.Fatal(err)
	}
	job := f.job(t)
	if _, err := f.a.Sync(t.Context(), job.Request); err == nil {
		t.Fatal("expected publication transaction failure")
	}
	repo, err := f.store.LoadRepository(t.Context(), f.repo.TenantID, f.repo.ID)
	if err != nil || repo.ArchiveHeadID != "" || repo.ArchiveVersion != 0 {
		t.Fatalf("partially published head=%+v err=%v", repo, err)
	}
	var snapshots, published, outbox int
	if err := f.pool.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM source_snapshots),
		(SELECT count(*) FROM source_storage_manifests WHERE status='published'),
		(SELECT count(*) FROM outbox)`).Scan(&snapshots, &published, &outbox); err != nil || snapshots != 0 || published != 0 || outbox != 0 {
		t.Fatalf("partial publication: snapshots=%d published=%d outbox=%d err=%v", snapshots, published, outbox, err)
	}
	if _, err := f.pool.Exec(t.Context(), `DROP TRIGGER reject_source_snapshot ON source_snapshots`); err != nil {
		t.Fatal(err)
	}
	expireSourceLease(t, f, job)
	f.git.disabled = true
	result, err := f.a.Sync(t.Context(), job.Request)
	if err != nil || result.SnapshotCount != 1 {
		t.Fatalf("candidate publication=%+v err=%v", result, err)
	}
	data, err := f.a.Payloads.Get(t.Context(), f.repo.TenantID, result.ResultRef)
	if err != nil {
		t.Fatal(err)
	}
	var candidate source.Candidate
	if err := json.Unmarshal(data, &candidate); err != nil {
		t.Fatal(err)
	}
	known, err := f.store.KnownCommits(t.Context(), f.repo.TenantID, f.repo.ID)
	if err != nil || len(known) != 1 || !candidate.Snapshots[0].CreatedAt.Equal(known[candidate.Snapshots[0].CommitSHA].CreatedAt) {
		t.Fatalf("persisted snapshot time mismatch: candidate=%+v known=%+v err=%v", candidate.Snapshots, known, err)
	}
}
