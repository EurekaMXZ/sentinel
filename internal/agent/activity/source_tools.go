package activity

import (
	openaigo "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"

	"sentinel/internal/source"
)

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func integerProperty(description string, minimum, maximum int) map[string]any {
	property := map[string]any{"type": "integer", "description": description, "minimum": minimum}
	if maximum > 0 {
		property["maximum"] = maximum
	}
	return property
}

// SourceToolSet returns the server-side registry entries for source tools.
func SourceToolSet() map[string]Tool {
	tools := make(map[string]Tool, len(source.ToolNames()))
	for _, definition := range SourceToolDefinitions() {
		tools[definition.Name] = Tool{Definition: definition, Source: true}
	}
	return tools
}

// SourceToolDefinitions are model-visible tool definitions. Execution is
// dispatched to the Source Worker; the Agent Worker only validates arguments
// and access.
func SourceToolDefinitions() []responses.FunctionToolParam {
	return []responses.FunctionToolParam{
		{
			Name:        source.ToolListVersions,
			Strict:      openaigo.Bool(false),
			Description: openaigo.String("List immutable, ready source snapshots of a bound repository, newest first."),
			Parameters: map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"repository_id"},
				"properties": map[string]any{
					"repository_id": stringProperty("Bound repository UUID."),
					"cursor":        stringProperty("Opaque next_cursor from the previous page."),
					"limit":         integerProperty("Maximum versions to return.", 1, 200),
				},
			},
		},
		{
			Name:        source.ToolListFiles,
			Strict:      openaigo.Bool(false),
			Description: openaigo.String("List one directory level (or a single file) in a ready source snapshot."),
			Parameters: map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"source_snapshot_id"},
				"properties": map[string]any{
					"source_snapshot_id": stringProperty("Ready snapshot UUID."),
					"path":               stringProperty("Repository-relative directory or file path; empty means the root."),
					"cursor":             stringProperty("Opaque next_cursor from the previous page."),
					"limit":              integerProperty("Maximum entries to return.", 1, 500),
				},
			},
		},
		{
			Name:        source.ToolReadFile,
			Strict:      openaigo.Bool(false),
			Description: openaigo.String("Read a line range of a text file from a ready source snapshot."),
			Parameters: map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"source_snapshot_id", "path"},
				"properties": map[string]any{
					"source_snapshot_id": stringProperty("Ready snapshot UUID."),
					"path":               stringProperty("Repository-relative file path."),
					"start_line":         integerProperty("First line, 1-based; defaults to 1.", 1, 0),
					"end_line":           integerProperty("Last line, inclusive; defaults to the read limit.", 1, 0),
				},
			},
		},
		{
			Name:        source.ToolSearch,
			Strict:      openaigo.Bool(false),
			Description: openaigo.String("Search a ready source snapshot for a literal string, returning matching lines."),
			Parameters: map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"source_snapshot_id", "query"},
				"properties": map[string]any{
					"source_snapshot_id": stringProperty("Ready snapshot UUID."),
					"query":              stringProperty("Literal text to search for."),
					"path":               stringProperty("Optional repository-relative directory or file filter."),
					"cursor":             stringProperty("Opaque next_cursor from the previous page."),
					"limit":              integerProperty("Maximum matching lines to return.", 1, 200),
				},
			},
		},
		{
			Name:        source.ToolDiff,
			Strict:      openaigo.Bool(false),
			Description: openaigo.String("Compare two ready snapshots of the same repository as a unified patch."),
			Parameters: map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"base_snapshot_id", "target_snapshot_id"},
				"properties": map[string]any{
					"base_snapshot_id":   stringProperty("Base snapshot UUID."),
					"target_snapshot_id": stringProperty("Target snapshot UUID."),
					"path":               stringProperty("Optional repository-relative path filter."),
					"cursor":             stringProperty("Opaque next_cursor from the previous page."),
					"limit":              integerProperty("Maximum patch lines to return.", 1, 2000),
				},
			},
		},
	}
}
