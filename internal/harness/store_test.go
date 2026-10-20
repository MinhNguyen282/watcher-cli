package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStoreRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewStore(dir)

	// A missing harness loads as a default, not an error.
	h, err := store.LoadHarness()
	require.NoError(t, err)
	require.Equal(t, "base", h.ID)

	in := &HarnessState{
		ID:           "evolved",
		SystemPrompt: "Be careful with migrations.",
		Guardrails:   []string{"Never edit tests."},
		ToolGuidance: map[string]string{"bash": "Run `go test ./...` after edits."},
		Memory:       []string{"The worker pool is in pkg/worker."},
		Scripts:      map[string]string{"check.sh": "#!/bin/sh\ngo test ./...\n"},
	}
	require.NoError(t, store.SaveHarness(in))

	out, err := store.LoadHarness()
	require.NoError(t, err)
	require.Equal(t, "evolved", out.ID)
	require.Equal(t, []string{"Never edit tests."}, out.Guardrails)
	require.Contains(t, out.RenderedSystemPrompt, "guardrails")
	require.Contains(t, out.RenderedSystemPrompt, "Never edit tests.")

	// The rendered section survives a re-save unchanged.
	rendered := out.RenderedSystemPrompt
	require.NoError(t, store.SaveHarness(out))
	require.Equal(t, rendered, out.RenderedSystemPrompt)
}

func TestStoreLessons(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewStore(dir)

	lessons, err := store.LoadLessons()
	require.NoError(t, err)
	require.Empty(t, lessons)

	require.NoError(t, store.AppendLesson("Run focused tests, not the whole suite."))
	require.NoError(t, store.AppendLesson("Read CONTRIBUTING.md before editing docs."))

	lessons, err = store.LoadLessons()
	require.NoError(t, err)
	require.Contains(t, lessons, "focused tests")
	require.Contains(t, lessons, "CONTRIBUTING.md")

	// Empty lessons are no-ops.
	require.NoError(t, store.AppendLesson("   "))
	lessons, err = store.LoadLessons()
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(lessons, "## "))
}

func TestStoreTrace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewStore(dir)

	trace := &ExecutionTrace{
		ProbeID:    "cand-1",
		HarnessID:  "cand-1",
		TaskPrompt: "Fix the race.",
		ToolCalls:  []ToolCallRecord{{Name: "bash", Input: "go test ./..."}},
		GitDiff:    "diff --git a/x b/x",
	}
	require.NoError(t, store.SaveTrace(trace))

	data, err := os.ReadFile(filepath.Join(dir, DirName, tracesDirName, "cand-1.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"probe_id": "cand-1"`)
}

func TestStoreWriteScripts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewStore(dir)

	h := &HarnessState{Scripts: map[string]string{
		"check.sh":       "#!/bin/sh\n",
		"../escape.sh":   "path traversal, must be skipped",
		"nested/ok.sh":   "nested path, must be skipped",
		"C:\\win.sh":     "backslash path, must be skipped",
	}}
	require.NoError(t, store.WriteScripts(h))

	script := filepath.Join(dir, DirName, scriptsDirName, "check.sh")
	data, err := os.ReadFile(script)
	require.NoError(t, err)
	require.Contains(t, string(data), "#!/bin/sh")

	entries, err := os.ReadDir(filepath.Join(dir, DirName, scriptsDirName))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestSanitizeClampsSizes(t *testing.T) {
	t.Parallel()
	var guardrails []string
	for i := 0; i < 100; i++ {
		guardrails = append(guardrails, "rule")
	}
	long := strings.Repeat("x", maxFieldLen*2)
	h := (&HarnessState{
		ID:         long,
		Guardrails: guardrails,
		Memory:     []string{long},
	}).Sanitize()

	require.LessOrEqual(t, len(h.ID), maxFieldLen)
	require.Len(t, h.Guardrails, maxGuardrails)
	require.LessOrEqual(t, len(h.Memory[0]), maxFieldLen)
}

func TestRenderSectionEmpty(t *testing.T) {
	t.Parallel()
	require.Equal(t, "", (&HarnessState{ID: "x"}).RenderSection())
	require.Equal(t, "", (*HarnessState)(nil).RenderSection())
}

func TestRenderSectionBounded(t *testing.T) {
	t.Parallel()
	h := &HarnessState{SystemPrompt: strings.Repeat("y", maxTotalRender*2)}
	rendered := h.RenderSection()
	require.LessOrEqual(t, len(rendered), maxTotalRender)
	require.Contains(t, rendered, "<self-evolving-harness>")
}
