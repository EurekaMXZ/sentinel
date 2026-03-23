package git_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sentinel/internal/source"
	sourcegit "sentinel/internal/source/git"
)

func writeTreeFile(t *testing.T, dir, name string, body []byte) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
}

func commitTree(t *testing.T, dir, message string) string {
	t.Helper()
	command(t, dir, "add", "-A")
	command(t, dir, "commit", "-m", message)
	return command(t, dir, "rev-parse", "HEAD")
}

func readWorkspace(t *testing.T) (source.Workspace, string) {
	t.Helper()
	remote := remoteFixture(t)
	command(t, remote, "config", "core.autocrlf", "false")
	writeTreeFile(t, remote, "root.txt", []byte("alpha\nbeta\ngamma\n"))
	writeTreeFile(t, remote, "dir/a.txt", []byte("one\ntwo\n"))
	writeTreeFile(t, remote, "dir/sub/b.txt", []byte("deep\n"))
	writeTreeFile(t, remote, "binary.bin", []byte{'a', 0, 'b'})
	command(t, remote, "add", "-A")
	command(t, remote, "commit", "-m", "first")
	first := command(t, remote, "rev-parse", "HEAD")
	writeTreeFile(t, remote, "root.txt", []byte("alpha\nBETA\ngamma\ndelta\n"))
	command(t, remote, "add", "-A")
	command(t, remote, "commit", "-m", "second")
	second := command(t, remote, "rev-parse", "HEAD")
	client := sourcegit.Client{Binary: "git", CacheDir: t.TempDir()}
	workspace := openWorkspace(t, client)
	if _, err := workspace.Fetch(t.Context(), remote, []string{"refs/heads/*"}, ""); err != nil {
		t.Fatal(err)
	}
	return workspace, first + ":" + second
}

func splitCommits(t *testing.T, value string) (string, string) {
	t.Helper()
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		t.Fatal("invalid commit pair")
	}
	return parts[0], parts[1]
}

func TestReadFilesSearchAndDiff(t *testing.T) {
	workspace, commits := readWorkspace(t)
	first, second := splitCommits(t, commits)
	ctx := t.Context()
	if present, err := workspace.HasCommit(ctx, first); err != nil || !present {
		t.Fatalf("first commit present=%t err=%v", present, err)
	}
	if present, err := workspace.HasCommit(ctx, "1234567890abcdef1234567890abcdef12345678"); err != nil || present {
		t.Fatalf("missing commit present=%t err=%v", present, err)
	}
	entries, more, err := workspace.ListFiles(ctx, first, "", 0, 100)
	if err != nil || more {
		t.Fatalf("root entries=%+v more=%t err=%v", entries, more, err)
	}
	byName := make(map[string]source.FileEntry)
	for _, entry := range entries {
		byName[entry.Path] = entry
	}
	if byName["dir"].Kind != "directory" || byName["root.txt"].Kind != "file" || byName["binary.bin"].Kind != "file" {
		t.Fatalf("unexpected root entries: %+v", entries)
	}
	entries, more, err = workspace.ListFiles(ctx, first, "dir", 0, 100)
	if err != nil || more || len(entries) != 2 || entries[0].Path != "dir/a.txt" || entries[1].Path != "dir/sub" {
		t.Fatalf("directory entries=%+v more=%t err=%v", entries, more, err)
	}
	entries, more, err = workspace.ListFiles(ctx, first, "", 0, 2)
	if err != nil || !more || len(entries) != 2 {
		t.Fatalf("first page=%+v more=%t err=%v", entries, more, err)
	}
	entries, more, err = workspace.ListFiles(ctx, first, "", 2, 2)
	if err != nil || more || len(entries) != 1 || entries[0].Path != "root.txt" {
		t.Fatalf("second page=%+v more=%t err=%v", entries, more, err)
	}
	if _, _, err := workspace.ListFiles(ctx, first, "missing", 0, 10); !errors.Is(err, source.ErrPathNotFound) {
		t.Fatalf("missing path error=%v", err)
	}

	content, start, end, truncated, err := workspace.ReadFile(ctx, first, "root.txt", 2, 3, 10, 1024)
	if err != nil || content != "beta\ngamma\n" || start != 2 || end != 3 || truncated {
		t.Fatalf("read file content=%q start=%d end=%d truncated=%t err=%v", content, start, end, truncated, err)
	}
	content, start, end, truncated, err = workspace.ReadFile(ctx, first, "root.txt", 1, 0, 2, 1024)
	if err != nil || content != "alpha\nbeta\n" || start != 1 || end != 2 || !truncated {
		t.Fatalf("limited read content=%q start=%d end=%d truncated=%t err=%v", content, start, end, truncated, err)
	}
	for _, test := range []struct {
		path string
		want error
	}{
		{"missing.txt", source.ErrPathNotFound},
		{"dir", source.ErrNotFile},
		{"binary.bin", source.ErrBinaryFile},
	} {
		if _, _, _, _, err := workspace.ReadFile(ctx, first, test.path, 1, 1, 10, 1024); !errors.Is(err, test.want) {
			t.Errorf("%s error=%v want=%v", test.path, err, test.want)
		}
	}
	if _, _, _, _, err := workspace.ReadFile(ctx, first, "../root.txt", 1, 1, 10, 1024); err == nil {
		t.Fatal("accepted a path traversal")
	}
	if _, _, _, _, err := workspace.ReadFile(ctx, first, "root.txt", 20, 21, 10, 1024); !errors.Is(err, source.ErrLineRange) {
		t.Fatalf("line range error=%v", err)
	}

	matches, more, truncated, err := workspace.Search(ctx, first, "beta", "", 0, 50, 4096)
	if err != nil || len(matches) != 1 || more || truncated ||
		matches[0].Location.Path != "root.txt" || matches[0].Location.StartLine != 2 || matches[0].Content != "beta" {
		t.Fatalf("search matches=%+v more=%t truncated=%t err=%v", matches, more, truncated, err)
	}
	matches, more, _, err = workspace.Search(ctx, first, "a", "", 0, 2, 4096)
	if err != nil || len(matches) != 2 || !more {
		t.Fatalf("search page=%+v more=%t err=%v", matches, more, err)
	}
	matches, more, _, err = workspace.Search(ctx, first, "a", "", 2, 2, 4096)
	if err != nil || len(matches) != 1 || more {
		t.Fatalf("search second page=%+v more=%t err=%v", matches, more, err)
	}
	matches, _, _, err = workspace.Search(ctx, first, "o", "dir", 0, 50, 4096)
	if err != nil || len(matches) != 2 || !strings.HasPrefix(matches[0].Location.Path, "dir/") {
		t.Fatalf("path search matches=%+v err=%v", matches, err)
	}

	patch, consumed, more, truncated, err := workspace.Diff(ctx, first, second, "root.txt", 0, 500, 65536)
	if err != nil || !strings.Contains(patch, "-beta") || !strings.Contains(patch, "+BETA") ||
		!strings.Contains(patch, "+delta") || consumed == 0 || more || truncated {
		t.Fatalf("diff patch=%q consumed=%d more=%t truncated=%t err=%v", patch, consumed, more, truncated, err)
	}
	patch, consumed, more, _, err = workspace.Diff(ctx, first, second, "root.txt", 0, 2, 65536)
	if err != nil || !more || consumed == 0 || len(strings.SplitAfter(patch, "\n")) > 3 {
		t.Fatalf("limited diff patch=%q consumed=%d more=%t err=%v", patch, consumed, more, err)
	}
	if !bytes.Contains([]byte(patch), []byte("\n")) {
		t.Fatalf("limited diff lost line boundaries: %q", patch)
	}
}
