package tools

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"charm.land/fantasy"
)

// DefaultMaxResultChars is the default ceiling for a tool result that is
// sent back to the model. Tools whose output is naturally large (bash, view,
// grep) raise it in the table below; every result above the ceiling is cut
// with an explicit marker instead of being silently accepted.
const DefaultMaxResultChars = 100_000

// Backgrounding describes whether a tool may be detached from the turn.
type Backgrounding int

const (
	// BackgroundingAllowed lets the caller background the tool when it is
	// supported by the tool body.
	BackgroundingAllowed Backgrounding = iota
	// BackgroundingNever marks tools that must run inside the turn. A
	// deferred tool that writes or mutates state belongs here.
	BackgroundingNever
)

func (b Backgrounding) String() string {
	if b == BackgroundingNever {
		return "never"
	}
	return "allowed"
}

// Spec is the per-tool contract the harness relies on. It is deliberately
// separate from fantasy.ToolInfo: the provider schema is the model-facing
// surface, while the spec is the harness-facing surface used by the
// permission engine, the dispatch pipeline and the tool index.
type Spec struct {
	// Name is the registered tool name, as sent to the provider.
	Name string
	// RuleContentField is the input field that acts as the content of a
	// permission rule (for example "file_path" or "command"). Empty means
	// the tool has no content-scoped rules and only tool-wide rules apply.
	RuleContentField string
	// ReadOnly is true when the tool cannot change files, processes or
	// external state. Read-only tools may run in parallel and are exempt
	// from mutating-mode restrictions.
	ReadOnly bool
	// Mutating is the explicit opposite of ReadOnly for tools that write.
	// It exists so the plan-mode gate can deny by intent, not by omission.
	Mutating bool
	// SearchHint is a one-line capability phrase used by tool search when
	// the schema is deferred.
	SearchHint string
	// Deferrable marks tools whose schema can be withheld from the initial
	// request and loaded through tool search instead.
	Deferrable bool
	// MaxResultChars bounds the content returned to the model. Zero means
	// DefaultMaxResultChars.
	MaxResultChars int
	// Backgrounding states whether the tool may be detached.
	Backgrounding Backgrounding
}

// ResultLimit returns the effective content ceiling for the spec.
func (s Spec) ResultLimit() int {
	if s.MaxResultChars > 0 {
		return s.MaxResultChars
	}
	return DefaultMaxResultChars
}

// toolSpecs is the single source of truth for harness-facing tool metadata.
// Adding a tool here is what makes it visible to permission rules, plan mode
// and result ceilings; a tool that is registered but absent falls back to a
// conservative default (mutating, no content rule, never backgrounded).
var toolSpecs = map[string]Spec{
	EnterPlanModeToolName: {
		Name:          EnterPlanModeToolName,
		SearchHint:    "switch the session into read-only plan mode",
		Backgrounding: BackgroundingNever,
	},
	ExitPlanModeToolName: {
		Name:             ExitPlanModeToolName,
		RuleContentField: "plan",
		SearchHint:       "present a plan and leave plan mode",
		Backgrounding:    BackgroundingNever,
	},
	RepoMapToolName: {
		Name:       RepoMapToolName,
		ReadOnly:   true,
		SearchHint: "look up symbols and relationships in the repository map",
	},
	AgenticFetchToolName: {
		Name:             AgenticFetchToolName,
		RuleContentField: "url",
		ReadOnly:         true,
		SearchHint:       "fetch a URL and summarize it with a sub-agent",
	},
	WebFetchToolName: {
		Name:             WebFetchToolName,
		RuleContentField: "url",
		ReadOnly:         true,
		SearchHint:       "fetch content from a URL",
	},
	WebSearchToolName: {
		Name:             WebSearchToolName,
		RuleContentField: "query",
		ReadOnly:         true,
		SearchHint:       "search the web",
	},
	BashToolName: {
		Name:             BashToolName,
		RuleContentField: "command",
		Mutating:         true,
		SearchHint:       "run a shell command",
		MaxResultChars:   200_000,
		Backgrounding:    BackgroundingAllowed,
	},
	VerifyToolName: {
		Name:             VerifyToolName,
		RuleContentField: "packages",
		Mutating:         true,
		SearchHint:       "run the project's own checks for changed code",
		MaxResultChars:   200_000,
	},
	CrushInfoToolName: {
		Name:       CrushInfoToolName,
		ReadOnly:   true,
		SearchHint: "read the current runtime state",
	},
	CrushLogsToolName: {
		Name:       CrushLogsToolName,
		ReadOnly:   true,
		SearchHint: "read the application log",
	},
	JobOutputToolName: {
		Name:       JobOutputToolName,
		ReadOnly:   true,
		SearchHint: "read the output of a background job",
	},
	JobKillToolName: {
		Name:          JobKillToolName,
		Mutating:      true,
		SearchHint:    "terminate a background job",
		Backgrounding: BackgroundingNever,
	},
	DownloadToolName: {
		Name:             DownloadToolName,
		RuleContentField: "url",
		Mutating:         true,
		SearchHint:       "download a URL to a local file",
	},
	EditToolName: {
		Name:             EditToolName,
		RuleContentField: "file_path",
		Mutating:         true,
		SearchHint:       "edit a file with an exact string replacement",
		Backgrounding:    BackgroundingNever,
	},
	MultiEditToolName: {
		Name:             MultiEditToolName,
		RuleContentField: "file_path",
		Mutating:         true,
		SearchHint:       "apply several exact replacements to one file",
		Backgrounding:    BackgroundingNever,
	},
	ViewToolName: {
		Name:             ViewToolName,
		RuleContentField: "file_path",
		ReadOnly:         true,
		SearchHint:       "read a file, image or PDF",
		MaxResultChars:   200_000,
	},
	GlobToolName: {
		Name:       GlobToolName,
		ReadOnly:   true,
		SearchHint: "find files by glob pattern",
	},
	GrepToolName: {
		Name:       GrepToolName,
		ReadOnly:   true,
		SearchHint: "search file contents",
	},
	LSToolName: {
		Name:       LSToolName,
		ReadOnly:   true,
		SearchHint: "list a directory",
	},
	SourcegraphToolName: {
		Name:             SourcegraphToolName,
		RuleContentField: "query",
		ReadOnly:         true,
		SearchHint:       "search public code on Sourcegraph",
	},
	TodosToolName: {
		Name:          TodosToolName,
		Mutating:      true,
		SearchHint:    "update the task list",
		Backgrounding: BackgroundingNever,
	},
	WriteToolName: {
		Name:             WriteToolName,
		RuleContentField: "file_path",
		Mutating:         true,
		SearchHint:       "write a file",
		Backgrounding:    BackgroundingNever,
	},
	QuestionToolName: {
		Name:             QuestionToolName,
		RuleContentField: "question",
		SearchHint:       "ask the user a structured question",
		Backgrounding:    BackgroundingNever,
	},
	DiagnosticsToolName: {
		Name:       DiagnosticsToolName,
		ReadOnly:   true,
		SearchHint: "read language-server diagnostics",
	},
	ReferencesToolName: {
		Name:       ReferencesToolName,
		ReadOnly:   true,
		SearchHint: "find references to a symbol",
	},
	LSPRestartToolName: {
		Name:          LSPRestartToolName,
		Mutating:      true,
		SearchHint:    "restart a language server",
		Backgrounding: BackgroundingNever,
	},
	SymbolsToolName: {
		Name:       SymbolsToolName,
		ReadOnly:   true,
		SearchHint: "list document symbols",
	},
	DefinitionToolName: {
		Name:       DefinitionToolName,
		ReadOnly:   true,
		SearchHint: "find a symbol definition",
	},
	CallHierarchyToolName: {
		Name:       CallHierarchyToolName,
		ReadOnly:   true,
		SearchHint: "inspect callers and callees",
	},
	RenameToolName: {
		Name:             RenameToolName,
		RuleContentField: "symbol",
		Mutating:         true,
		SearchHint:       "rename a symbol across files",
		Backgrounding:    BackgroundingNever,
	},
	ReplaceSymbolToolName: {
		Name:             ReplaceSymbolToolName,
		RuleContentField: "symbol",
		Mutating:         true,
		SearchHint:       "replace a whole symbol by name",
		Backgrounding:    BackgroundingNever,
	},
	ListMCPResourcesToolName: {
		Name:       ListMCPResourcesToolName,
		ReadOnly:   true,
		SearchHint: "list MCP server resources",
	},
	ReadMCPResourceToolName: {
		Name:             ReadMCPResourceToolName,
		RuleContentField: "uri",
		ReadOnly:         true,
		SearchHint:       "read an MCP server resource",
	},
	DBSchemaToolName: {
		Name:       DBSchemaToolName,
		ReadOnly:   true,
		SearchHint: "list database tables and columns",
	},
	DBQueryToolName: {
		Name:             DBQueryToolName,
		RuleContentField: "query",
		ReadOnly:         true,
		SearchHint:       "run a read-only SQL query",
	},
	OpenAPISpecToolName: {
		Name:             OpenAPISpecToolName,
		RuleContentField: "path",
		ReadOnly:         true,
		SearchHint:       "summarize an OpenAPI specification",
	},
}

// SpecFor returns the spec for a tool. Unknown tools (MCP tools, plugins)
// get a conservative default: mutating, no content rule, never backgrounded.
// The second return value reports whether the spec was declared.
func SpecFor(name string) (Spec, bool) {
	if spec, ok := toolSpecs[name]; ok {
		return spec, true
	}
	return Spec{
		Name:          name,
		Mutating:      true,
		Backgrounding: BackgroundingNever,
	}, false
}

// Specs returns every declared spec, ordered by name for stable iteration.
func Specs() []Spec {
	out := make([]Spec, 0, len(toolSpecs))
	for _, spec := range toolSpecs {
		out = append(out, spec)
	}
	slices.SortFunc(out, func(a, b Spec) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// IsReadOnly reports whether the tool cannot change state. Unknown tools are
// treated as mutating.
func IsReadOnly(name string) bool {
	spec, _ := SpecFor(name)
	return spec.ReadOnly
}

// IsMutating reports whether the tool can change state. Unknown tools are
// treated as mutating.
func IsMutating(name string) bool {
	spec, _ := SpecFor(name)
	return !spec.ReadOnly
}

// RuleContentFor extracts the permission rule content from a tool call's raw
// JSON input using the tool's RuleContentField. It returns "" when the tool
// has no content field, the input is not an object, or the field is absent.
func RuleContentFor(name string, input string) string {
	spec, _ := SpecFor(name)
	if spec.RuleContentField == "" || strings.TrimSpace(input) == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(input), &payload); err != nil {
		return ""
	}
	value, ok := payload[spec.RuleContentField]
	if !ok {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	default:
		return fmt.Sprint(v)
	}
}

// TruncateResult enforces the tool's result ceiling. Media responses are
// left alone: their bytes are not model-visible text and cutting them would
// corrupt the payload. The marker names the ceiling so the model knows the
// result was cut by the harness rather than by the tool.
func TruncateResult(name string, response fantasy.ToolResponse) (fantasy.ToolResponse, bool) {
	if len(response.Data) > 0 || response.MediaType != "" {
		return response, false
	}
	spec, _ := SpecFor(name)
	limit := spec.ResultLimit()
	if limit <= 0 || len(response.Content) <= limit {
		return response, false
	}
	cut := limit
	// Prefer a rune boundary so the marker does not split UTF-8.
	for cut > 0 && !utf8.RuneStart(response.Content[cut]) {
		cut--
	}
	if cut == 0 {
		cut = limit
	}
	response.Content = response.Content[:cut] + fmt.Sprintf(
		"\n\n[result truncated by harness: %d of %d chars shown; narrow the call to see the rest]",
		cut, len(response.Content),
	)
	return response, true
}
