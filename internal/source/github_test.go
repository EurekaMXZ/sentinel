package source_test

import (
	"testing"

	"sentinel/internal/source"
)

func TestGitHubRemoteAndRefValidation(t *testing.T) {
	for _, remote := range []string{"https://github.com/owner/repo", "https://github.com/owner/repo.git/"} {
		if got, err := source.NormalizeGitHubURL(remote); err != nil || got != "https://github.com/owner/repo.git" {
			t.Fatalf("remote=%q err=%v", got, err)
		}
	}
	for _, remote := range []string{"git@github.com:owner/repo", "http://github.com/owner/repo", "https://token@github.com/owner/repo", "https://github.com.evil.test/owner/repo", "https://github.com:443/owner/repo", "https://github.com/owner/repo?token=secret", "https://github.com/owner/../repo", "https://github.com/owner/%72epo", "https://github.com/owner/repo#fragment"} {
		if _, err := source.NormalizeGitHubURL(remote); err == nil {
			t.Errorf("accepted remote %q", remote)
		}
	}
	for _, ref := range []string{"refs/heads/*", "refs/tags/v*", "refs/heads/team/main"} {
		if !source.ValidTrackedRef(ref) {
			t.Errorf("rejected ref %q", ref)
		}
	}
	for _, ref := range []string{"--all", "refs/heads/../main", "refs/heads/.private", "refs/heads/a.lock", "refs/heads/a\nb", "refs/heads/a\x01b", "refs/heads/*/*", "refs/pull/*", "refs/heads/a^{commit}"} {
		if source.ValidTrackedRef(ref) {
			t.Errorf("accepted ref %q", ref)
		}
	}
	if !source.MatchesRef("refs/heads/*", "refs/heads/team/main") || source.MatchesRef("refs/tags/*", "refs/heads/main") {
		t.Fatal("ref matching")
	}
	for _, ref := range []string{"literal-token", "env:", "env:GITHUB_TOKEN\n", "env:../secret"} {
		if source.ValidCredentialRef(ref) {
			t.Errorf("accepted credential reference %q", ref)
		}
	}
}
