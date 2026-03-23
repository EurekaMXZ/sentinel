package agent_test

import (
	"testing"

	"sentinel/internal/agent"
)

func TestResourceAccessScopes(t *testing.T) {
	repo := "0194e229-8576-7f42-8f08-3496913847cf"
	other := "0194e229-8576-7f42-8f08-3496913847ce"
	snapshot := "0194e229-8576-7f42-8f08-3496913847cd"
	repositoryBinding := agent.ResourceBinding{BindingName: "repo", Kind: agent.ResourceSourceRepository, RepositoryID: repo}
	snapshotBinding := agent.ResourceBinding{BindingName: "snap", Kind: agent.ResourceSourceSnapshot,
		RepositoryID: repo, SourceSnapshotID: snapshot}

	if !agent.CanAccess([]agent.ResourceBinding{repositoryBinding}, agent.ResourceBinding{
		Kind: agent.ResourceSourceSnapshot, RepositoryID: repo, SourceSnapshotID: snapshot}) {
		t.Fatal("repository binding did not grant snapshot access")
	}
	if agent.CanAccess([]agent.ResourceBinding{snapshotBinding}, agent.ResourceBinding{
		Kind: agent.ResourceSourceSnapshot, RepositoryID: repo, SourceSnapshotID: "0194e229-8576-7f42-8f08-3496913847cc"}) {
		t.Fatal("snapshot binding granted access to a different snapshot")
	}
	if !agent.CanAccessRepository([]agent.ResourceBinding{snapshotBinding}, repo) ||
		agent.CanAccessRepository([]agent.ResourceBinding{snapshotBinding}, other) {
		t.Fatal("repository access derived from a snapshot binding is incorrect")
	}
	if agent.CanDelegate([]agent.ResourceBinding{repositoryBinding}, agent.ResourceBinding{
		Kind: agent.ResourceSourceRepository, RepositoryID: other}) {
		t.Fatal("repository delegation crossed repositories")
	}
}
