package activity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	sdkactivity "go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"sentinel/internal/source"
)

const (
	defaultVersionLimit = 50
	maxVersionLimit     = 200
	defaultEntryLimit   = 100
	maxEntryLimit       = 500
	defaultSearchLimit  = 50
	maxSearchLimit      = 200
	defaultDiffLines    = 500
	maxDiffLines        = 2000
	maxReadFileLines    = 2000
	maxToolResultBytes  = 512 << 10
	maxPageContentBytes = 256 << 10
)

type sourceToolError struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Read performs one bounded read against a ready snapshot. User-level problems
// (missing paths, denied snapshots) become durable tool errors so the model can
// recover; storage, Git, and archive failures remain retryable Activity errors.
func (a *Activities) Read(ctx context.Context, request source.ToolRequest) (source.ToolResult, error) {
	if err := request.Validate(); err != nil {
		return source.ToolResult{}, temporal.NewNonRetryableApplicationError("invalid source tool request", "source_invalid", err)
	}
	if a.Reads == nil || a.Payloads == nil || a.Git == nil {
		return source.ToolResult{}, errors.New("source read runtime is not configured")
	}
	stop := readHeartbeat(ctx)
	defer stop()
	switch request.Name {
	case source.ToolListVersions:
		return a.listVersions(ctx, request)
	case source.ToolListFiles:
		return a.listFiles(ctx, request)
	case source.ToolReadFile:
		return a.readFile(ctx, request)
	case source.ToolSearch:
		return a.search(ctx, request)
	case source.ToolDiff:
		return a.diff(ctx, request)
	default:
		return source.ToolResult{}, temporal.NewNonRetryableApplicationError("unsupported source tool", "source_invalid", nil)
	}
}

func readHeartbeat(ctx context.Context) func() {
	if !sdkactivity.IsActivity(ctx) {
		return func() {}
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	sdkactivity.RecordHeartbeat(ctx)
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				sdkactivity.RecordHeartbeat(ctx)
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (a *Activities) readFailure(ctx context.Context, request source.ToolRequest, code, message string) (source.ToolResult, error) {
	data, err := json.Marshal(sourceToolError{OK: false, Code: code, Message: message})
	if err != nil {
		return source.ToolResult{}, err
	}
	ref, err := a.Payloads.Put(ctx, request.TenantID, "source-tool-result", data)
	if err != nil {
		return source.ToolResult{}, err
	}
	return source.ToolResult{ResultRef: ref, Failed: true, Code: code}, nil
}

func (a *Activities) saveReadResult(ctx context.Context, request source.ToolRequest, value any) (source.ToolResult, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return source.ToolResult{}, err
	}
	if len(data) > maxToolResultBytes {
		return a.readFailure(ctx, request, "source_result_too_large", "the requested page exceeds the tool result limit")
	}
	ref, err := a.Payloads.Put(ctx, request.TenantID, "source-tool-result", data)
	if err != nil {
		return source.ToolResult{}, err
	}
	return source.ToolResult{ResultRef: ref}, nil
}

func pageLimit(value, fallback, maximum int) int {
	if value <= 0 {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}

type readCursor struct {
	Version int    `json:"v"`
	Kind    string `json:"kind"`
	Key     string `json:"key"`
	Offset  int    `json:"offset"`
}

func cursorKey(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func encodeCursor(kind, key string, offset int) string {
	data, err := json.Marshal(readCursor{Version: 1, Kind: kind, Key: key, Offset: offset})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeCursor(cursor, kind, key string) (int, string) {
	if cursor == "" {
		return 0, ""
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, "source_invalid_cursor"
	}
	var decoded readCursor
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.Version != 1 ||
		decoded.Kind != kind || decoded.Key != key || decoded.Offset < 0 {
		return 0, "source_invalid_cursor"
	}
	return decoded.Offset, ""
}

func locationOf(snapshot source.Snapshot, path string, startLine, endLine int) source.Location {
	return source.Location{RepositoryID: snapshot.RepositoryID, SourceSnapshotID: snapshot.ID,
		CommitSHA: snapshot.CommitSHA, Path: path, StartLine: startLine, EndLine: endLine}
}

func (a *Activities) listVersions(ctx context.Context, request source.ToolRequest) (source.ToolResult, error) {
	limit := pageLimit(request.Limit, defaultVersionLimit, maxVersionLimit)
	key := cursorKey(source.ToolListVersions, request.TenantID, request.RepositoryID, strconv.Itoa(limit))
	offset, code := decodeCursor(request.Cursor, source.ToolListVersions, key)
	if code != "" {
		return a.readFailure(ctx, request, code, "the cursor does not belong to this query")
	}
	if _, err := a.Reads.LoadRepository(ctx, request.TenantID, request.RepositoryID); errors.Is(err, source.ErrNotFound) {
		return a.readFailure(ctx, request, "source_repository_not_found", "the repository is not available")
	} else if err != nil {
		return source.ToolResult{}, err
	}
	snapshots, more, err := a.Reads.ListReadySnapshots(ctx, request.TenantID, request.RepositoryID, offset, limit)
	if err != nil {
		return source.ToolResult{}, err
	}
	page := source.VersionPage{RepositoryID: request.RepositoryID, Versions: snapshots}
	if more && len(snapshots) > 0 {
		page.NextCursor = encodeCursor(source.ToolListVersions, key, offset+len(snapshots))
	}
	return a.saveReadResult(ctx, request, page)
}

func (a *Activities) resolveSnapshot(ctx context.Context, request source.ToolRequest, snapshotID string) (source.Snapshot, source.Repository, *source.ToolResult, error) {
	snapshot, err := a.Reads.LoadSnapshot(ctx, request.TenantID, request.RepositoryID, snapshotID)
	if errors.Is(err, source.ErrNotFound) {
		failure, putErr := a.readFailure(ctx, request, "source_snapshot_not_found", "the snapshot is not available")
		return source.Snapshot{}, source.Repository{}, &failure, putErr
	}
	if err != nil {
		return source.Snapshot{}, source.Repository{}, nil, err
	}
	if snapshot.Status != source.SnapshotReady {
		failure, putErr := a.readFailure(ctx, request, "source_snapshot_not_ready", "the snapshot is not ready for reading")
		return source.Snapshot{}, source.Repository{}, &failure, putErr
	}
	repository, err := a.Reads.LoadRepository(ctx, request.TenantID, snapshot.RepositoryID)
	if errors.Is(err, source.ErrNotFound) {
		failure, putErr := a.readFailure(ctx, request, "source_repository_not_found", "the repository is not available")
		return source.Snapshot{}, source.Repository{}, &failure, putErr
	}
	if err != nil {
		return source.Snapshot{}, source.Repository{}, nil, err
	}
	return snapshot, repository, nil, nil
}

func (a *Activities) openReadWorkspace(ctx context.Context, tenant string, repository source.Repository) (source.Workspace, error) {
	workspace, err := a.Git.Open(ctx, tenant, repository.ID, repository.ObjectFormat)
	if err != nil {
		return nil, err
	}
	if _, err := a.restore(ctx, workspace, repository); err != nil {
		_ = workspace.Close()
		return nil, err
	}
	return workspace, nil
}

func ensureCommit(ctx context.Context, workspace source.Workspace, oid string) error {
	present, err := workspace.HasCommit(ctx, oid)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("restored archive is missing commit %s", oid)
	}
	return nil
}

func (a *Activities) listFiles(ctx context.Context, request source.ToolRequest) (source.ToolResult, error) {
	snapshot, repository, failure, err := a.resolveSnapshot(ctx, request, request.SourceSnapshotID)
	if err != nil || failure != nil {
		if failure != nil {
			return *failure, nil
		}
		return source.ToolResult{}, err
	}
	workspace, err := a.openReadWorkspace(ctx, request.TenantID, repository)
	if err != nil {
		return source.ToolResult{}, err
	}
	defer workspace.Close()
	if err := ensureCommit(ctx, workspace, snapshot.CommitSHA); err != nil {
		return source.ToolResult{}, err
	}
	limit := pageLimit(request.Limit, defaultEntryLimit, maxEntryLimit)
	key := cursorKey(source.ToolListFiles, request.TenantID, snapshot.ID, request.Path, strconv.Itoa(limit))
	offset, code := decodeCursor(request.Cursor, source.ToolListFiles, key)
	if code != "" {
		return a.readFailure(ctx, request, code, "the cursor does not belong to this query")
	}
	entries, more, err := workspace.ListFiles(ctx, snapshot.CommitSHA, request.Path, offset, limit)
	if errors.Is(err, source.ErrPathNotFound) {
		return a.readFailure(ctx, request, "source_path_not_found", "the path does not exist in this snapshot")
	}
	if err != nil {
		return source.ToolResult{}, err
	}
	if entries == nil {
		entries = []source.FileEntry{}
	}
	location := locationOf(snapshot, request.Path, 0, 0)
	for len(entries) > 0 {
		data, err := json.Marshal(source.FilePage{Location: location, Entries: entries})
		if err != nil {
			return source.ToolResult{}, err
		}
		if len(data) <= maxToolResultBytes {
			break
		}
		entries = entries[:len(entries)-1]
		more = true
	}
	if len(entries) == 0 && more {
		return a.readFailure(ctx, request, "source_result_too_large", "the requested page exceeds the tool result limit")
	}
	page := source.FilePage{Location: location, Entries: entries}
	if more {
		page.NextCursor = encodeCursor(source.ToolListFiles, key, offset+len(entries))
	}
	return a.saveReadResult(ctx, request, page)
}

func (a *Activities) readFile(ctx context.Context, request source.ToolRequest) (source.ToolResult, error) {
	snapshot, repository, failure, err := a.resolveSnapshot(ctx, request, request.SourceSnapshotID)
	if err != nil || failure != nil {
		if failure != nil {
			return *failure, nil
		}
		return source.ToolResult{}, err
	}
	workspace, err := a.openReadWorkspace(ctx, request.TenantID, repository)
	if err != nil {
		return source.ToolResult{}, err
	}
	defer workspace.Close()
	if err := ensureCommit(ctx, workspace, snapshot.CommitSHA); err != nil {
		return source.ToolResult{}, err
	}
	content, startLine, endLine, truncated, err := workspace.ReadFile(ctx, snapshot.CommitSHA, request.Path,
		request.StartLine, request.EndLine, maxReadFileLines, maxPageContentBytes)
	switch {
	case errors.Is(err, source.ErrPathNotFound):
		return a.readFailure(ctx, request, "source_path_not_found", "the path does not exist in this snapshot")
	case errors.Is(err, source.ErrNotFile):
		return a.readFailure(ctx, request, "source_not_a_file", "the path is not a regular file")
	case errors.Is(err, source.ErrBinaryFile):
		return a.readFailure(ctx, request, "source_binary_file", "the file is binary")
	case errors.Is(err, source.ErrFileTooLarge):
		return a.readFailure(ctx, request, "source_file_too_large", "the file is too large to read in one call")
	case errors.Is(err, source.ErrLineRange):
		return a.readFailure(ctx, request, "source_line_range", "the requested line range is invalid")
	case err != nil:
		return source.ToolResult{}, err
	}
	return a.saveReadResult(ctx, request, source.FileContent{
		Location: locationOf(snapshot, request.Path, startLine, endLine), Content: content, Truncated: truncated})
}

func (a *Activities) search(ctx context.Context, request source.ToolRequest) (source.ToolResult, error) {
	snapshot, repository, failure, err := a.resolveSnapshot(ctx, request, request.SourceSnapshotID)
	if err != nil || failure != nil {
		if failure != nil {
			return *failure, nil
		}
		return source.ToolResult{}, err
	}
	workspace, err := a.openReadWorkspace(ctx, request.TenantID, repository)
	if err != nil {
		return source.ToolResult{}, err
	}
	defer workspace.Close()
	if err := ensureCommit(ctx, workspace, snapshot.CommitSHA); err != nil {
		return source.ToolResult{}, err
	}
	limit := pageLimit(request.Limit, defaultSearchLimit, maxSearchLimit)
	key := cursorKey(source.ToolSearch, request.TenantID, snapshot.ID, request.Path, request.Query, strconv.Itoa(limit))
	offset, code := decodeCursor(request.Cursor, source.ToolSearch, key)
	if code != "" {
		return a.readFailure(ctx, request, code, "the cursor does not belong to this query")
	}
	matches, more, truncated, err := workspace.Search(ctx, snapshot.CommitSHA, request.Query, request.Path,
		offset, limit, maxPageContentBytes)
	if err != nil {
		return source.ToolResult{}, err
	}
	if matches == nil {
		matches = []source.SearchMatch{}
	}
	for i := range matches {
		matches[i].Location.RepositoryID = snapshot.RepositoryID
		matches[i].Location.SourceSnapshotID = snapshot.ID
		matches[i].Location.CommitSHA = snapshot.CommitSHA
	}
	page := source.SearchPage{Matches: matches, Truncated: truncated}
	for len(page.Matches) > 0 {
		data, err := json.Marshal(page)
		if err != nil {
			return source.ToolResult{}, err
		}
		if len(data) <= maxToolResultBytes {
			break
		}
		page.Matches = page.Matches[:len(page.Matches)-1]
		more = true
	}
	if len(page.Matches) == 0 && more {
		return a.readFailure(ctx, request, "source_result_too_large", "the requested page exceeds the tool result limit")
	}
	if more {
		page.NextCursor = encodeCursor(source.ToolSearch, key, offset+len(page.Matches))
	}
	return a.saveReadResult(ctx, request, page)
}

func (a *Activities) diff(ctx context.Context, request source.ToolRequest) (source.ToolResult, error) {
	base, repository, failure, err := a.resolveSnapshot(ctx, request, request.BaseSnapshotID)
	if err != nil || failure != nil {
		if failure != nil {
			return *failure, nil
		}
		return source.ToolResult{}, err
	}
	target, targetRepository, failure, err := a.resolveSnapshot(ctx, request, request.TargetSnapshotID)
	if err != nil || failure != nil {
		if failure != nil {
			return *failure, nil
		}
		return source.ToolResult{}, err
	}
	if targetRepository.ID != repository.ID || base.RepositoryID != target.RepositoryID {
		return a.readFailure(ctx, request, "source_repository_mismatch", "both snapshots must belong to the same repository")
	}
	workspace, err := a.openReadWorkspace(ctx, request.TenantID, repository)
	if err != nil {
		return source.ToolResult{}, err
	}
	defer workspace.Close()
	if err := ensureCommit(ctx, workspace, base.CommitSHA); err != nil {
		return source.ToolResult{}, err
	}
	if err := ensureCommit(ctx, workspace, target.CommitSHA); err != nil {
		return source.ToolResult{}, err
	}
	limit := pageLimit(request.Limit, defaultDiffLines, maxDiffLines)
	key := cursorKey(source.ToolDiff, request.TenantID, base.ID, target.ID, request.Path, strconv.Itoa(limit))
	offset, code := decodeCursor(request.Cursor, source.ToolDiff, key)
	if code != "" {
		return a.readFailure(ctx, request, code, "the cursor does not belong to this query")
	}
	patch, consumed, more, truncated, err := workspace.Diff(ctx, base.CommitSHA, target.CommitSHA, request.Path,
		offset, limit, maxPageContentBytes)
	if err != nil {
		return source.ToolResult{}, err
	}
	page := source.DiffPage{Base: locationOf(base, request.Path, 0, 0),
		Target: locationOf(target, request.Path, 0, 0), Patch: patch, Truncated: truncated}
	if more {
		page.NextCursor = encodeCursor(source.ToolDiff, key, offset+consumed)
	}
	return a.saveReadResult(ctx, request, page)
}
