package source

import (
	"errors"
	"strings"
)

const (
	ToolListVersions = "source_list_versions"
	ToolListFiles    = "source_list_files"
	ToolReadFile     = "source_read_file"
	ToolSearch       = "source_search"
	ToolDiff         = "source_diff"

	// ReadActivity runs on the Source Worker's task queue.
	ReadActivity = "sentinel.source.read"
)

func ToolNames() []string {
	return []string{ToolListVersions, ToolListFiles, ToolReadFile, ToolSearch, ToolDiff}
}

func IsTool(name string) bool {
	switch name {
	case ToolListVersions, ToolListFiles, ToolReadFile, ToolSearch, ToolDiff:
		return true
	}
	return false
}

// ToolRequest is the normalized, immutable dispatch request for a source read.
// The Agent Worker resolves access before dispatch; the Source Worker verifies
// tenant, repository, and snapshot readiness again.
type ToolRequest struct {
	TenantID         string `json:"tenant_id"`
	AgentID          string `json:"agent_id"`
	OperationID      string `json:"operation_id"`
	ToolCallID       string `json:"tool_call_id"`
	Name             string `json:"name"`
	RepositoryID     string `json:"repository_id,omitempty"`
	SourceSnapshotID string `json:"source_snapshot_id,omitempty"`
	BaseSnapshotID   string `json:"base_snapshot_id,omitempty"`
	TargetSnapshotID string `json:"target_snapshot_id,omitempty"`
	Path             string `json:"path,omitempty"`
	Query            string `json:"query,omitempty"`
	StartLine        int    `json:"start_line,omitempty"`
	EndLine          int    `json:"end_line,omitempty"`
	Cursor           string `json:"cursor,omitempty"`
	Limit            int    `json:"limit,omitempty"`
}

func (r ToolRequest) Validate() error {
	if !ValidID(r.TenantID) || !ValidID(r.AgentID) || strings.TrimSpace(r.OperationID) == "" ||
		strings.TrimSpace(r.ToolCallID) == "" || !IsTool(r.Name) {
		return errors.New("invalid source tool identity")
	}
	if !ValidID(r.RepositoryID) || !ValidSourcePath(r.Path) || len(r.Cursor) > 4096 ||
		r.Limit < 0 || r.Limit > 1000 || r.StartLine < 0 || r.EndLine < 0 ||
		(r.EndLine > 0 && r.StartLine > 0 && r.EndLine < r.StartLine) {
		return errors.New("invalid source tool parameters")
	}
	switch r.Name {
	case ToolListVersions:
		if r.SourceSnapshotID != "" || r.BaseSnapshotID != "" || r.TargetSnapshotID != "" || r.Query != "" {
			return errors.New("invalid list-versions request")
		}
	case ToolListFiles, ToolReadFile, ToolSearch:
		if !ValidID(r.SourceSnapshotID) || r.BaseSnapshotID != "" || r.TargetSnapshotID != "" {
			return errors.New("invalid snapshot tool request")
		}
		if r.Name == ToolSearch && !ValidSourceQuery(r.Query) {
			return errors.New("invalid source search query")
		}
		if r.Name != ToolSearch && r.Query != "" {
			return errors.New("unexpected source search query")
		}
		if r.Name == ToolReadFile && strings.TrimSpace(r.Path) == "" {
			return errors.New("source file path is required")
		}
	case ToolDiff:
		if !ValidID(r.BaseSnapshotID) || !ValidID(r.TargetSnapshotID) ||
			r.SourceSnapshotID != "" || r.Query != "" {
			return errors.New("invalid source diff request")
		}
	}
	return nil
}

// ValidSourcePath rejects absolute paths, traversal, backslashes, and control
// characters. The path is always passed to Git as a literal pathspec.
func ValidSourcePath(path string) bool {
	if path == "" {
		return true
	}
	if len(path) > 4096 || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return false
	}
	for _, ch := range path {
		if ch < 32 || ch == 127 {
			return false
		}
	}
	// A single trailing slash is a natural way to name a directory and is
	// accepted by Git's literal pathspec handling.
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func ValidSourceQuery(query string) bool {
	return strings.TrimSpace(query) != "" && len(query) <= 1024 &&
		!strings.ContainsAny(query, "\x00\r\n")
}

// ToolResult carries only a reference and status back to the Agent Workflow.
// The full result body stays in object storage.
type ToolResult struct {
	ResultRef string `json:"result_ref"`
	Failed    bool   `json:"failed,omitempty"`
	Code      string `json:"code,omitempty"`
}
