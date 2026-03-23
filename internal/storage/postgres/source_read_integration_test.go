//go:build integration

package postgres_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sentinel/internal/source"
)

func writeSourceTree(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func commitSourceTree(t *testing.T, f *sourceFixture, message string) string {
	t.Helper()
	sourceCommand(t, f.git.remote, "add", "-A")
	sourceCommand(t, f.git.remote, "commit", "-m", message)
	return sourceCommand(t, f.git.remote, "rev-parse", "HEAD")
}

func readPayload(t *testing.T, f *sourceFixture, result source.ToolResult) string {
	t.Helper()
	data, err := f.a.Payloads.Get(t.Context(), f.repo.TenantID, result.ResultRef)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSourceReadTools(t *testing.T) {
	f := newSourceFixture(t)
	sourceCommand(t, f.git.remote, "config", "core.autocrlf", "false")
	writeSourceTree(t, f.git.remote, "root.txt", "alpha\nbeta\ngamma\n")
	writeSourceTree(t, f.git.remote, "dir/a.txt", "one\ntwo\n")
	writeSourceTree(t, f.git.remote, "dir/sub/b.txt", "deep\n")
	firstCommit := commitSourceTree(t, f, "first")
	firstSync := f.synchronize(t)
	if firstSync.SnapshotCount != 1 {
		t.Fatalf("first sync: %+v", firstSync)
	}
	writeSourceTree(t, f.git.remote, "root.txt", "alpha\nBETA\ngamma\ndelta\n")
	secondCommit := commitSourceTree(t, f, "second")
	secondSync := f.synchronize(t)
	if secondSync.SnapshotCount != 1 {
		t.Fatalf("second sync: %+v", secondSync)
	}
	snapshots, _, err := f.store.ListReadySnapshots(t.Context(), f.repo.TenantID, f.repo.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	byCommit := make(map[string]source.Snapshot)
	for _, snapshot := range snapshots {
		byCommit[snapshot.CommitSHA] = snapshot
	}
	first, second := byCommit[firstCommit], byCommit[secondCommit]
	if first.ID == "" || second.ID == "" {
		t.Fatalf("snapshots=%+v", snapshots)
	}
	baseRequest := source.ToolRequest{TenantID: f.repo.TenantID, AgentID: newID(t),
		OperationID: "op", ToolCallID: "call", RepositoryID: f.repo.ID}

	versions := baseRequest
	versions.Name = source.ToolListVersions
	result, err := f.a.Read(t.Context(), versions)
	if err != nil || result.Failed || result.ResultRef == "" {
		t.Fatalf("list versions result=%+v err=%v", result, err)
	}
	var versionPage source.VersionPage
	if err := json.Unmarshal([]byte(readPayload(t, f, result)), &versionPage); err != nil ||
		versionPage.RepositoryID != f.repo.ID || len(versionPage.Versions) != 2 {
		t.Fatalf("versions=%+v err=%v", versionPage, err)
	}

	files := baseRequest
	files.Name = source.ToolListFiles
	files.SourceSnapshotID = first.ID
	files.Path = "dir"
	result, err = f.a.Read(t.Context(), files)
	if err != nil || result.Failed {
		t.Fatalf("list files result=%+v err=%v", result, err)
	}
	var filePage source.FilePage
	if err := json.Unmarshal([]byte(readPayload(t, f, result)), &filePage); err != nil ||
		len(filePage.Entries) != 2 || filePage.Location.CommitSHA != firstCommit {
		t.Fatalf("files=%+v err=%v", filePage, err)
	}

	read := baseRequest
	read.Name = source.ToolReadFile
	read.SourceSnapshotID = first.ID
	read.Path = "root.txt"
	read.StartLine, read.EndLine = 2, 3
	result, err = f.a.Read(t.Context(), read)
	if err != nil || result.Failed {
		t.Fatalf("read file result=%+v err=%v", result, err)
	}
	var content source.FileContent
	if err := json.Unmarshal([]byte(readPayload(t, f, result)), &content); err != nil ||
		content.Content != "beta\ngamma\n" || content.Location.SourceSnapshotID != first.ID {
		t.Fatalf("content=%+v err=%v", content, err)
	}

	search := baseRequest
	search.Name = source.ToolSearch
	search.SourceSnapshotID = second.ID
	search.Query = "BETA"
	result, err = f.a.Read(t.Context(), search)
	if err != nil || result.Failed {
		t.Fatalf("search result=%+v err=%v", result, err)
	}
	var searchPage source.SearchPage
	if err := json.Unmarshal([]byte(readPayload(t, f, result)), &searchPage); err != nil ||
		len(searchPage.Matches) != 1 || searchPage.Matches[0].Location.StartLine != 2 {
		t.Fatalf("search=%+v err=%v", searchPage, err)
	}

	diff := baseRequest
	diff.Name = source.ToolDiff
	diff.BaseSnapshotID, diff.TargetSnapshotID = first.ID, second.ID
	result, err = f.a.Read(t.Context(), diff)
	if err != nil || result.Failed {
		t.Fatalf("diff result=%+v err=%v", result, err)
	}
	var diffPage source.DiffPage
	if err := json.Unmarshal([]byte(readPayload(t, f, result)), &diffPage); err != nil ||
		!strings.Contains(diffPage.Patch, "+BETA") || !strings.Contains(diffPage.Patch, "+delta") {
		t.Fatalf("diff=%+v err=%v", diffPage, err)
	}

	missing := baseRequest
	missing.Name = source.ToolReadFile
	missing.SourceSnapshotID = newID(t)
	missing.Path = "root.txt"
	result, err = f.a.Read(t.Context(), missing)
	if err != nil || !result.Failed || result.Code != "source_snapshot_not_found" {
		t.Fatalf("missing snapshot result=%+v err=%v", result, err)
	}
	traversal := missing
	traversal.SourceSnapshotID = first.ID
	traversal.Path = "../root.txt"
	// The Agent rejects traversal before dispatch; the Source Worker must too.
	if _, err := f.a.Read(t.Context(), traversal); err == nil {
		t.Fatal("Source Worker accepted a path traversal")
	}
}
