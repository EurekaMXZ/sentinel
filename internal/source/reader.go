package source

import "context"

// Limits constrain both returned items/lines and serialized output bytes.
type Limits struct {
	MaxBytes int64 `json:"max_bytes"`
	MaxLines int   `json:"max_lines"`
}

type Location struct {
	RepositoryID     string `json:"repository_id"`
	SourceSnapshotID string `json:"source_snapshot_id"`
	CommitSHA        string `json:"commit_sha"`
	Path             string `json:"path"`
	StartLine        int    `json:"start_line,omitempty"`
	EndLine          int    `json:"end_line,omitempty"`
}

type ListFilesRequest struct {
	SourceSnapshotID string `json:"source_snapshot_id"`
	Path             string `json:"path"`
	Page             Page   `json:"page"`
	Limits           Limits `json:"limits"`
}

type FileEntry struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	ObjectSHA string `json:"object_sha"`
}

type FilePage struct {
	Location   Location    `json:"location"`
	Entries    []FileEntry `json:"entries"`
	NextCursor string      `json:"next_cursor,omitempty"`
	Truncated  bool        `json:"truncated"`
}

type ReadFileRequest struct {
	SourceSnapshotID string `json:"source_snapshot_id"`
	Path             string `json:"path"`
	StartLine        int    `json:"start_line"`
	EndLine          int    `json:"end_line"`
	Limits           Limits `json:"limits"`
}

type FileContent struct {
	Location  Location `json:"location"`
	Content   string   `json:"content"`
	Truncated bool     `json:"truncated"`
}

type SearchRequest struct {
	SourceSnapshotID string `json:"source_snapshot_id"`
	Query            string `json:"query"`
	Path             string `json:"path"`
	Page             Page   `json:"page"`
	Limits           Limits `json:"limits"`
}

type SearchMatch struct {
	Location Location `json:"location"`
	Content  string   `json:"content"`
}

type SearchPage struct {
	Matches    []SearchMatch `json:"matches"`
	NextCursor string        `json:"next_cursor,omitempty"`
	Truncated  bool          `json:"truncated"`
}

type DiffRequest struct {
	BaseSnapshotID   string `json:"base_snapshot_id"`
	TargetSnapshotID string `json:"target_snapshot_id"`
	Path             string `json:"path"`
	Page             Page   `json:"page"`
	Limits           Limits `json:"limits"`
}

type DiffPage struct {
	Base       Location `json:"base"`
	Target     Location `json:"target"`
	Patch      string   `json:"patch"`
	NextCursor string   `json:"next_cursor,omitempty"`
	Truncated  bool     `json:"truncated"`
}

// Reader is used only inside Activities. Persist large results and return references
// to Workflows; pass bounded excerpts to the model. Diff must use the same repository.
type Reader interface {
	ListFiles(ctx context.Context, request ListFilesRequest) (FilePage, error)
	ReadFile(ctx context.Context, request ReadFileRequest) (FileContent, error)
	Search(ctx context.Context, request SearchRequest) (SearchPage, error)
	Diff(ctx context.Context, request DiffRequest) (DiffPage, error)
}
