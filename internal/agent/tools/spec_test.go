package tools

import (
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func TestSpecsAreConsistent(t *testing.T) {
	t.Parallel()

	specs := Specs()
	require.NotEmpty(t, specs)

	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		require.NotEmpty(t, spec.Name, "spec name must be set")
		names = append(names, spec.Name)
		require.False(t,
			spec.ReadOnly && spec.Mutating,
			"%s cannot be both read-only and mutating", spec.Name,
		)
	}

	// File mutators must run inside the turn; only long-running shell work
	// is allowed to detach.
	for _, name := range []string{EditToolName, MultiEditToolName, WriteToolName, RenameToolName, ReplaceSymbolToolName} {
		spec, ok := SpecFor(name)
		require.True(t, ok, "%s must have a declared spec", name)
		require.Equal(t, BackgroundingNever, spec.Backgrounding, "%s must not be backgrounded", name)
	}

	require.Len(t, names, len(uniqueStrings(names)), "spec names must be unique")
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func TestSpecForUnknownToolIsConservative(t *testing.T) {
	t.Parallel()

	spec, declared := SpecFor("mcp__server__tool")
	require.False(t, declared)
	require.True(t, spec.Mutating)
	require.False(t, spec.ReadOnly)
	require.Equal(t, BackgroundingNever, spec.Backgrounding)
	require.Empty(t, spec.RuleContentField)
	require.Equal(t, DefaultMaxResultChars, spec.ResultLimit())
}

func TestReadOnlyToolsAreClassified(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		ViewToolName, GrepToolName, GlobToolName, LSToolName,
		DiagnosticsToolName, SymbolsToolName, DefinitionToolName,
		ReferencesToolName, CallHierarchyToolName, RepoMapToolName,
		JobOutputToolName, DBSchemaToolName, DBQueryToolName,
	} {
		require.True(t, IsReadOnly(name), "%s should be read-only", name)
		require.False(t, IsMutating(name), "%s should not be mutating", name)
	}

	for _, name := range []string{
		EditToolName, MultiEditToolName, WriteToolName, BashToolName,
		DownloadToolName, RenameToolName, ReplaceSymbolToolName, JobKillToolName,
	} {
		require.True(t, IsMutating(name), "%s should be mutating", name)
		require.False(t, IsReadOnly(name), "%s should not be read-only", name)
	}
}

func TestRuleContentFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"edit", `{"file_path":"a/b.go","old_string":"x","new_string":"y"}`, "a/b.go"},
		{"bash", `{"command":"git status","description":"check"}`, "git status"},
		{"view", `{"file_path":"/tmp/x"}`, "/tmp/x"},
		{"view", `{"offset":1}`, ""},
		{"bash", `not json`, ""},
		{"todos", `{"todos":[]}`, ""},
	}

	for _, tt := range tests {
		require.Equal(t, tt.want, RuleContentFor(tt.name, tt.input), "%s %s", tt.name, tt.input)
	}
}

func TestTruncateResult(t *testing.T) {
	t.Parallel()

	t.Run("under limit is untouched", func(t *testing.T) {
		resp := fantasy.NewTextResponse(strings.Repeat("a", 10))
		got, truncated := TruncateResult(ViewToolName, resp)
		require.False(t, truncated)
		require.Equal(t, resp.Content, got.Content)
	})

	t.Run("over limit is cut with a marker", func(t *testing.T) {
		content := strings.Repeat("a", DefaultMaxResultChars+500)
		resp := fantasy.NewTextResponse(content)
		got, truncated := TruncateResult("mcp__unknown", resp)
		require.True(t, truncated)
		require.Less(t, len(got.Content), DefaultMaxResultChars+200)
		require.True(t, strings.HasPrefix(got.Content, strings.Repeat("a", 100)))
		require.Contains(t, got.Content, "[result truncated by harness:")
	})

	t.Run("media responses are never cut", func(t *testing.T) {
		payload := make([]byte, DefaultMaxResultChars+1)
		resp := fantasy.NewImageResponse(payload, "image/png")
		got, truncated := TruncateResult("mcp__unknown", resp)
		require.False(t, truncated)
		require.Len(t, got.Data, len(payload))
	})

	t.Run("invalid utf8 boundary is respected", func(t *testing.T) {
		// "é" is two bytes; force the cut inside the rune.
		content := strings.Repeat("a", DefaultMaxResultChars-1) + "éé"
		got, truncated := TruncateResult("mcp__unknown", fantasy.NewTextResponse(content))
		require.True(t, truncated)
		require.NotContains(t, got.Content, "\ufffd")
	})
}
