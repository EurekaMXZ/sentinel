package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"sentinel/internal/source"
	sourcegit "sentinel/internal/source/git"
)

func command(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func remoteFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is required")
	}
	dir := t.TempDir()
	command(t, dir, "init", "-b", "main")
	command(t, dir, "config", "user.name", "Source Test")
	command(t, dir, "config", "user.email", "source@example.test")
	return dir
}

func commit(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "source.txt"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, dir, "add", "source.txt")
	command(t, dir, "commit", "-m", body)
	return command(t, dir, "rev-parse", "HEAD")
}

func openWorkspace(t *testing.T, client sourcegit.Client) source.Workspace {
	t.Helper()
	w, err := client.Open(t.Context(), uuid.NewString(), uuid.NewString(), "sha1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
	})
	return w
}

func TestBundlesRestoreHistoryAcrossForcePushAndDeletedRefs(t *testing.T) {
	remote := remoteFixture(t)
	first := commit(t, remote, "first version")
	command(t, remote, "tag", "-a", "v1", "-m", "version one")
	command(t, remote, "branch", "obsolete")
	client := sourcegit.Client{Binary: "git", CacheDir: t.TempDir()}
	w := openWorkspace(t, client)
	refs, err := w.Fetch(t.Context(), remote, []string{"refs/heads/*", "refs/tags/*"}, "")
	if err != nil || len(refs) != 3 {
		t.Fatalf("initial refs=%+v err=%v", refs, err)
	}
	for _, ref := range refs {
		if ref.CommitSHA != first {
			t.Fatalf("ref did not peel to commit: %+v", ref)
		}
	}
	base := filepath.Join(t.TempDir(), "base.bundle")
	info, err := w.Bundle(t.Context(), base, nil)
	if err != nil || info.Empty || len(info.Prerequisites) != 0 {
		t.Fatalf("base=%+v err=%v", info, err)
	}
	before, err := w.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second := commit(t, remote, "second version")
	if _, err := w.Fetch(t.Context(), remote, []string{"refs/heads/*", "refs/tags/*"}, ""); err != nil {
		t.Fatal(err)
	}
	increment := filepath.Join(t.TempDir(), "increment.bundle")
	info, err = w.Bundle(t.Context(), increment, before.Roots)
	if err != nil || info.Empty || !slices.Contains(info.Prerequisites, first) {
		t.Fatalf("increment=%+v err=%v", info, err)
	}
	before, err = w.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	command(t, remote, "tag", "-d", "v1")
	command(t, remote, "branch", "-D", "obsolete")
	command(t, remote, "checkout", "--orphan", "rewrite")
	command(t, remote, "rm", "-rf", ".")
	third := commit(t, remote, "rewritten version")
	command(t, remote, "branch", "-M", "main")
	refs, err = w.Fetch(t.Context(), remote, []string{"refs/heads/*", "refs/tags/*"}, "")
	if err != nil || len(refs) != 1 || refs[0].CommitSHA != third {
		t.Fatalf("rewritten refs=%+v err=%v", refs, err)
	}
	rewrite := filepath.Join(t.TempDir(), "rewrite.bundle")
	if info, err := w.Bundle(t.Context(), rewrite, before.Roots); err != nil || info.Empty {
		t.Fatalf("rewrite=%+v err=%v", info, err)
	}
	missingBase := openWorkspace(t, client)
	if err := missingBase.Import(t.Context(), increment); err == nil {
		t.Fatal("increment restored without its prerequisite")
	}
	restored := openWorkspace(t, client)
	for _, path := range []string{base, increment, rewrite} {
		if err := restored.Import(t.Context(), path); err != nil {
			t.Fatal(err)
		}
	}
	if err := restored.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := restored.State(t.Context())
	if err != nil || len(state.Commits) != 3 {
		t.Fatalf("restored state=%+v err=%v", state, err)
	}
	for sha, want := range map[string]string{first: "first version", second: "second version", third: "rewritten version"} {
		if got := command(t, restored.Directory(), "show", sha+":source.txt"); got != want {
			t.Fatalf("restored %s=%q want %q", sha, got, want)
		}
	}
	if info, err := restored.Bundle(t.Context(), filepath.Join(t.TempDir(), "noop.bundle"), state.Roots); err != nil || !info.Empty {
		t.Fatalf("unchanged archive=%+v err=%v", info, err)
	}
}

func TestEmptyRepositoryBlobTagsAndCancellation(t *testing.T) {
	remote := remoteFixture(t)
	client := sourcegit.Client{Binary: "git", CacheDir: t.TempDir()}
	w := openWorkspace(t, client)
	if refs, err := w.Fetch(t.Context(), remote, []string{"refs/heads/*", "refs/tags/*"}, ""); err != nil || len(refs) != 0 {
		t.Fatalf("empty refs=%+v err=%v", refs, err)
	}
	if info, err := w.Bundle(t.Context(), filepath.Join(t.TempDir(), "empty.bundle"), nil); err != nil || !info.Empty {
		t.Fatalf("empty bundle=%+v err=%v", info, err)
	}
	commit(t, remote, "blob tag")
	blob := command(t, remote, "rev-parse", "HEAD:source.txt")
	command(t, remote, "tag", "blob", blob)
	refs, err := w.Fetch(t.Context(), remote, []string{"refs/tags/*"}, "")
	if err != nil || len(refs) != 1 || refs[0].CommitSHA != "" {
		t.Fatalf("blob refs=%+v err=%v", refs, err)
	}
	base := filepath.Join(t.TempDir(), "blob.bundle")
	if info, err := w.Bundle(t.Context(), base, nil); err != nil || info.Empty {
		t.Fatalf("blob bundle=%+v err=%v", info, err)
	}
	restored := openWorkspace(t, client)
	if err := restored.Import(t.Context(), base); err != nil {
		t.Fatal(err)
	}
	if got := command(t, restored.Directory(), "cat-file", "blob", blob); got != "blob tag" {
		t.Fatal(got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := restored.Fetch(ctx, remote, []string{"refs/heads/*"}, ""); err == nil {
		t.Fatal("cancelled Git fetch succeeded")
	}
}

func TestGitHubSmoke(t *testing.T) {
	if os.Getenv("SENTINEL_TEST_GITHUB") != "1" {
		t.Skip("set SENTINEL_TEST_GITHUB=1 for a public GitHub smoke test")
	}
	w := openWorkspace(t, sourcegit.Client{Binary: "git", CacheDir: t.TempDir()})
	refs, err := w.Fetch(t.Context(), "https://github.com/octocat/Hello-World.git", []string{"refs/heads/*", "refs/tags/*"}, "")
	if err != nil || len(refs) == 0 {
		t.Fatalf("GitHub refs=%+v err=%v", refs, err)
	}
	if err := w.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := w.State(t.Context())
	if err != nil || len(state.Commits) == 0 {
		t.Fatalf("GitHub commits=%d err=%v", len(state.Commits), err)
	}
	t.Logf("GitHub: %d refs, %d commits", len(refs), len(state.Commits))
}
