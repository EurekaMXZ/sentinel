package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"sentinel/internal/source"
)

// A single blob must fit in memory for line slicing. Larger files remain
// searchable and diffable; read_file reports a bounded error instead.
const maxReadBlobBytes = 16 << 20

type treeEntry struct {
	oid  string
	kind string
	path string
}

func (w *workspace) HasCommit(ctx context.Context, oid string) (bool, error) {
	if !source.ValidOID(oid, w.format) {
		return false, errors.New("invalid commit OID")
	}
	_, code, err := w.execute(ctx, "", "", "cat-file", "-e", oid+"^{commit}")
	if err != nil {
		return false, err
	}
	return code == 0, nil
}

func (w *workspace) ListFiles(ctx context.Context, commit, path string, offset, limit int) ([]source.FileEntry, bool, error) {
	if offset < 0 || limit <= 0 || !source.ValidSourcePath(path) {
		return nil, false, errors.New("invalid file page")
	}
	entries, err := w.listTree(ctx, commit, path)
	if err != nil {
		return nil, false, err
	}
	if path != "" && len(entries) == 0 {
		return nil, false, source.ErrPathNotFound
	}
	if offset >= len(entries) {
		return nil, false, nil
	}
	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}
	result := make([]source.FileEntry, 0, end-offset)
	for _, entry := range entries[offset:end] {
		result = append(result, source.FileEntry{Path: entry.path, Kind: entry.kind, ObjectSHA: entry.oid})
	}
	return result, end < len(entries), nil
}

// listTree lists one directory level. Git returns the directory entry itself
// for "dir", so a trailing slash is used to expand it.
func (w *workspace) listTree(ctx context.Context, commit, path string) ([]treeEntry, error) {
	if !source.ValidOID(commit, w.format) {
		return nil, errors.New("invalid commit OID")
	}
	entries, err := w.lsTree(ctx, commit, path)
	if err != nil || path == "" {
		return entries, err
	}
	if len(entries) == 1 && entries[0].kind == "directory" && entries[0].path == path {
		return w.lsTree(ctx, commit, path+"/")
	}
	return entries, nil
}

func (w *workspace) lsTree(ctx context.Context, commit, path string) ([]treeEntry, error) {
	args := []string{"ls-tree", "-z", "--full-tree", commit}
	if path != "" {
		args = append(args, "--", ":(literal)"+path)
	}
	output, err := w.run(ctx, "", "", args...)
	if err != nil {
		return nil, err
	}
	entries := make([]treeEntry, 0)
	for _, record := range strings.Split(output, "\x00") {
		if record == "" {
			continue
		}
		meta, name, ok := strings.Cut(record, "\t")
		if !ok {
			return nil, errors.New("invalid tree entry")
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 || !source.ValidOID(fields[2], w.format) {
			return nil, errors.New("invalid tree entry metadata")
		}
		kind := ""
		switch fields[1] {
		case "blob":
			kind = "file"
		case "tree":
			kind = "directory"
		case "commit":
			kind = "submodule"
		default:
			return nil, errors.New("unsupported tree entry type")
		}
		entries = append(entries, treeEntry{oid: fields[2], kind: kind, path: name})
	}
	return entries, nil
}

func (w *workspace) ReadFile(ctx context.Context, commit, path string, startLine, endLine, maxLines, maxBytes int) (string, int, int, bool, error) {
	if strings.TrimSpace(path) == "" || !source.ValidSourcePath(path) || maxLines <= 0 || maxBytes <= 0 {
		return "", 0, 0, false, errors.New("invalid file read request")
	}
	entries, err := w.lsTree(ctx, commit, path)
	if err != nil {
		return "", 0, 0, false, err
	}
	if len(entries) == 0 {
		return "", 0, 0, false, source.ErrPathNotFound
	}
	if len(entries) != 1 || entries[0].path != path {
		return "", 0, 0, false, source.ErrNotFile
	}
	if entries[0].kind != "file" {
		return "", 0, 0, false, source.ErrNotFile
	}
	sizeOutput, err := w.run(ctx, "", "", "cat-file", "-s", entries[0].oid)
	if err != nil {
		return "", 0, 0, false, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(sizeOutput), 10, 64)
	if err != nil || size < 0 {
		return "", 0, 0, false, errors.New("invalid blob size")
	}
	if size > maxReadBlobBytes {
		return "", 0, 0, false, source.ErrFileTooLarge
	}
	content, err := w.run(ctx, "", "", "cat-file", "blob", entries[0].oid)
	if err != nil {
		return "", 0, 0, false, err
	}
	if bytes.IndexByte([]byte(content), 0) >= 0 {
		return "", 0, 0, false, source.ErrBinaryFile
	}
	lines := splitLinesAfter(content)
	if startLine == 0 {
		startLine = 1
	}
	if startLine < 1 {
		return "", 0, 0, false, source.ErrLineRange
	}
	openEnded := endLine == 0
	if openEnded {
		endLine = startLine + maxLines - 1
	}
	if endLine < startLine {
		return "", 0, 0, false, source.ErrLineRange
	}
	if len(lines) == 0 {
		return "", startLine, startLine - 1, false, nil
	}
	if startLine > len(lines) {
		return "", startLine, startLine - 1, false, source.ErrLineRange
	}
	requestedEnd := endLine
	truncated := openEnded && endLine < len(lines)
	if endLine-startLine+1 > maxLines {
		endLine = startLine + maxLines - 1
		truncated = true
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if endLine < startLine {
		return "", startLine, startLine - 1, false, source.ErrLineRange
	}
	var builder strings.Builder
	actualEnd := startLine - 1
	for i := startLine - 1; i < endLine; i++ {
		line := lines[i]
		if builder.Len()+len(line) > maxBytes {
			if builder.Len() == 0 {
				room := maxBytes
				if room > len(line) {
					room = len(line)
				}
				builder.WriteString(line[:room])
				actualEnd = i + 1
			}
			truncated = true
			break
		}
		builder.WriteString(line)
		actualEnd = i + 1
	}
	if actualEnd < requestedEnd && actualEnd < len(lines) {
		truncated = true
	}
	return builder.String(), startLine, actualEnd, truncated, nil
}

func (w *workspace) Search(ctx context.Context, commit, query, path string, offset, limit, maxBytes int) ([]source.SearchMatch, bool, bool, error) {
	if !source.ValidSourceQuery(query) || offset < 0 || limit <= 0 || maxBytes <= 0 || !source.ValidSourcePath(path) {
		return nil, false, false, errors.New("invalid source search")
	}
	args := []string{"grep", "-n", "-I", "-F", "-z", "--no-color", "-e", query, commit}
	if path != "" {
		args = append(args, "--", ":(literal)"+path)
	}
	output, code, err := w.execute(ctx, "", "", args...)
	if err != nil {
		return nil, false, false, err
	}
	if code == 1 {
		return nil, false, false, nil
	}
	if code != 0 {
		return nil, false, false, fmt.Errorf("git grep failed with status %d", code)
	}
	matches, err := parseGrep(output, commit)
	if err != nil {
		return nil, false, false, err
	}
	if offset >= len(matches) {
		return nil, false, false, nil
	}
	page := make([]source.SearchMatch, 0, limit)
	used := 0
	truncated := false
	next := offset
	for ; next < len(matches) && len(page) < limit; next++ {
		match := matches[next]
		size := len(match.Content) + len(match.Location.Path) + 64
		if used+size > maxBytes {
			if len(page) == 0 {
				if len(match.Content) > maxBytes {
					match.Content = match.Content[:maxBytes]
					truncated = true
				}
				page = append(page, match)
				next++
			}
			break
		}
		page = append(page, match)
		used += size
	}
	return page, next < len(matches), truncated, nil
}

func parseGrep(output, commit string) ([]source.SearchMatch, error) {
	matches := make([]source.SearchMatch, 0)
	rest := []byte(output)
	for len(rest) > 0 {
		pathEnd := bytes.IndexByte(rest, 0)
		if pathEnd < 0 {
			break
		}
		path := string(rest[:pathEnd])
		rest = rest[pathEnd+1:]
		lineEnd := bytes.IndexByte(rest, 0)
		if lineEnd < 0 {
			return nil, errors.New("invalid grep output")
		}
		lineText := string(rest[:lineEnd])
		rest = rest[lineEnd+1:]
		contentEnd := bytes.IndexByte(rest, '\n')
		var content string
		if contentEnd < 0 {
			content = string(rest)
			rest = nil
		} else {
			content = string(rest[:contentEnd])
			rest = rest[contentEnd+1:]
		}
		path = strings.TrimPrefix(path, commit+":")
		if !source.ValidSourcePath(path) || path == "" {
			continue
		}
		line, err := strconv.Atoi(lineText)
		if err != nil || line < 1 {
			return nil, errors.New("invalid grep line number")
		}
		matches = append(matches, source.SearchMatch{
			Location: source.Location{Path: path, StartLine: line, EndLine: line},
			Content:  content,
		})
	}
	return matches, nil
}

func (w *workspace) Diff(ctx context.Context, base, target, path string, offset, maxLines, maxBytes int) (string, int, bool, bool, error) {
	if !source.ValidOID(base, w.format) || !source.ValidOID(target, w.format) ||
		offset < 0 || maxLines <= 0 || maxBytes <= 0 || !source.ValidSourcePath(path) {
		return "", 0, false, false, errors.New("invalid source diff")
	}
	args := []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--unified=3", base, target}
	if path != "" {
		args = append(args, "--", ":(literal)"+path)
	}
	output, err := w.run(ctx, "", "", args...)
	if err != nil {
		return "", 0, false, false, err
	}
	lines := splitLinesAfter(output)
	if offset >= len(lines) {
		return "", 0, false, false, nil
	}
	var builder strings.Builder
	consumed := 0
	byteLimited := false
	for i := offset; i < len(lines) && consumed < maxLines; i++ {
		line := lines[i]
		if builder.Len()+len(line) > maxBytes {
			if builder.Len() == 0 {
				room := maxBytes
				if room > len(line) {
					room = len(line)
				}
				builder.WriteString(line[:room])
				consumed++
			}
			byteLimited = true
			break
		}
		builder.WriteString(line)
		consumed++
	}
	return builder.String(), consumed, offset+consumed < len(lines), byteLimited, nil
}

func splitLinesAfter(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.SplitAfter(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
