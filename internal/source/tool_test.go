package source_test

import (
	"testing"

	"sentinel/internal/source"
)

func TestSourcePathAndQueryValidation(t *testing.T) {
	for _, path := range []string{"", "root.txt", "dir/a.txt", "dir/", "dir/sub/"} {
		if !source.ValidSourcePath(path) {
			t.Errorf("rejected valid path %q", path)
		}
	}
	for _, path := range []string{"/etc/passwd", "../root.txt", "dir/../root.txt", "dir//a.txt",
		`dir\a.txt`, "dir/\x00a", "/"} {
		if source.ValidSourcePath(path) {
			t.Errorf("accepted invalid path %q", path)
		}
	}
	if source.ValidSourceQuery("") || source.ValidSourceQuery("bad\nquery") || !source.ValidSourceQuery("needle") {
		t.Fatal("query validation is incorrect")
	}
}

func TestSourceToolRequestValidation(t *testing.T) {
	tenant := "0194e229-8576-7f42-8f08-3496913847cf"
	agent := "0194e229-8576-7f42-8f08-3496913847ce"
	repository := "0194e229-8576-7f42-8f08-3496913847cd"
	snapshot := "0194e229-8576-7f42-8f08-3496913847cc"
	targetSnapshot := "0194e229-8576-7f42-8f08-3496913847cb"
	base := source.ToolRequest{TenantID: tenant, AgentID: agent, OperationID: "op", ToolCallID: "call",
		Name: source.ToolListVersions, RepositoryID: repository}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	for _, name := range source.ToolNames() {
		if !source.IsTool(name) {
			t.Errorf("tool %q is not recognized", name)
		}
	}
	cases := []source.ToolRequest{
		{},
		{TenantID: tenant, AgentID: agent, OperationID: "op", ToolCallID: "call", Name: "unknown", RepositoryID: repository},
		{TenantID: tenant, AgentID: agent, OperationID: "op", ToolCallID: "call", Name: source.ToolListVersions, RepositoryID: repository, SourceSnapshotID: snapshot},
		{TenantID: tenant, AgentID: agent, OperationID: "op", ToolCallID: "call", Name: source.ToolReadFile, RepositoryID: repository, SourceSnapshotID: snapshot},
		{TenantID: tenant, AgentID: agent, OperationID: "op", ToolCallID: "call", Name: source.ToolReadFile, RepositoryID: repository, SourceSnapshotID: snapshot, Path: "../x"},
		{TenantID: tenant, AgentID: agent, OperationID: "op", ToolCallID: "call", Name: source.ToolSearch, RepositoryID: repository, SourceSnapshotID: snapshot, Query: ""},
		{TenantID: tenant, AgentID: agent, OperationID: "op", ToolCallID: "call", Name: source.ToolDiff, RepositoryID: repository, BaseSnapshotID: snapshot},
	}
	for i, request := range cases {
		if err := request.Validate(); err == nil {
			t.Errorf("case %d unexpectedly validated: %+v", i, request)
		}
	}
	valid := source.ToolRequest{TenantID: tenant, AgentID: agent, OperationID: "op", ToolCallID: "call",
		Name: source.ToolDiff, RepositoryID: repository, BaseSnapshotID: snapshot, TargetSnapshotID: targetSnapshot}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid diff rejected: %v", err)
	}
}
