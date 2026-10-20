package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStripFences(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"```json\n{\"a\":1}\n```", `{"a":1}`},
		{"```\n{\"a\":1}\n```", `{"a":1}`},
		{"plain text", "plain text"},
		{"  {\"a\":1}  ", `{"a":1}`},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, stripFences(tc.in))
	}
}

func TestEditorProposeParsesCandidates(t *testing.T) {
	t.Parallel()
	chat := ChatFunc(func(_ context.Context, system, user string) (string, error) {
		require.Contains(t, system, "agent-optimization architect")
		require.Contains(t, user, "Base run trace")
		return `{"candidates":[
			{"id":"cand-1","tool_guidance":{"bash":"run focused tests"}},
			{"id":"cand-2","guardrails":["never edit tests"]}
		]}`, nil
	})
	editor := NewChatEditor(chat)

	base := DefaultHarness()
	trace := &ExecutionTrace{ProbeID: "base", TaskPrompt: "Fix the race"}
	candidates, err := editor.Propose(context.Background(), base, trace, 2)
	require.NoError(t, err)
	require.Len(t, candidates, 2)
	require.Equal(t, "cand-1", candidates[0].ID)
	require.Equal(t, "run focused tests", candidates[0].ToolGuidance["bash"])
	require.Equal(t, []string{"never edit tests"}, candidates[1].Guardrails)
}

func TestEditorProposeHandlesFences(t *testing.T) {
	t.Parallel()
	chat := ChatFunc(func(_ context.Context, _, _ string) (string, error) {
		return "```json\n{\"candidates\":[{\"id\":\"cand-1\"}]}\n```", nil
	})
	candidates, err := NewChatEditor(chat).Propose(context.Background(), DefaultHarness(), &ExecutionTrace{TaskPrompt: "x"}, 1)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
}

func TestEditorProposeRejectsGarbage(t *testing.T) {
	t.Parallel()
	chat := ChatFunc(func(_ context.Context, _, _ string) (string, error) {
		return "I am sorry, I cannot do that.", nil
	})
	_, err := NewChatEditor(chat).Propose(context.Background(), DefaultHarness(), &ExecutionTrace{TaskPrompt: "x"}, 1)
	require.Error(t, err)
}

func TestEditorComposeParsesResult(t *testing.T) {
	t.Parallel()
	chat := ChatFunc(func(_ context.Context, system, user string) (string, error) {
		require.Contains(t, system, "behavioral-contrast judge")
		require.Contains(t, user, "Candidate 1")
		return `{"best_probe_id":"cand-1","rationale":"it tested its work","harness":{"id":"merged","memory":["run focused tests"]}}`, nil
	})
	base := &ProbeOutcome{Trace: &ExecutionTrace{ProbeID: "base", TaskPrompt: "x"}, Harness: DefaultHarness()}
	cands := []ProbeOutcome{{Trace: &ExecutionTrace{ProbeID: "cand-1", HarnessID: "cand-1"}, Harness: DefaultHarness()}}

	res, err := NewChatEditor(chat).Compose(context.Background(), base, cands)
	require.NoError(t, err)
	require.Equal(t, "cand-1", res.BestProbeID)
	require.Equal(t, "merged", res.Harness.ID)
	require.Equal(t, []string{"run focused tests"}, res.Harness.Memory)
}

func TestEditorComposeRejectsMissingFields(t *testing.T) {
	t.Parallel()
	chat := ChatFunc(func(_ context.Context, _, _ string) (string, error) {
		return `{"rationale":"no winner"}`, nil
	})
	base := &ProbeOutcome{Trace: &ExecutionTrace{ProbeID: "base", TaskPrompt: "x"}, Harness: DefaultHarness()}
	_, err := NewChatEditor(chat).Compose(context.Background(), base, nil)
	require.Error(t, err)
}

func TestEditorReflectReturnsLesson(t *testing.T) {
	t.Parallel()
	chat := ChatFunc(func(_ context.Context, _, _ string) (string, error) {
		return "  Always run focused tests before editing.  ", nil
	})
	lesson, err := NewChatEditor(chat).Reflect(context.Background(), DefaultHarness(), DefaultHarness(),
		&ExecutionTrace{ProbeID: "base"}, &ExecutionTrace{ProbeID: "cand-1"})
	require.NoError(t, err)
	require.Equal(t, "Always run focused tests before editing.", lesson)
}

func TestFormatHarnessForLLM(t *testing.T) {
	t.Parallel()
	require.Equal(t, "(empty harness)", FormatHarnessForLLM(nil))
	formatted := FormatHarnessForLLM(&HarnessState{ID: "x", Guardrails: []string{"a"}})
	require.Contains(t, formatted, `"id": "x"`)
	require.Contains(t, formatted, "a")
}

func TestHeuristicBest(t *testing.T) {
	t.Parallel()
	base := &ProbeOutcome{
		Trace:   &ExecutionTrace{ProbeID: "base", HarnessID: "base"},
		Harness: DefaultHarness(),
	}
	good := ProbeOutcome{
		Trace: &ExecutionTrace{
			ProbeID:   "cand-1",
			HarnessID: "cand-1",
			GitDiff:   "diff",
			ToolCalls: []ToolCallRecord{{Name: "bash"}},
		},
		Harness: &HarnessState{ID: "cand-1"},
	}
	failed := ProbeOutcome{
		Trace:   &ExecutionTrace{ProbeID: "cand-2", HarnessID: "cand-2", Error: "boom"},
		Harness: &HarnessState{ID: "cand-2"},
	}
	best, refined, rationale := heuristicBest(base, []ProbeOutcome{failed, good})
	require.Equal(t, "cand-1", best.Trace.ProbeID)
	require.Equal(t, "merged", refined.ID)
	require.True(t, strings.Contains(rationale, "heuristic"))
}

func TestFindOutcome(t *testing.T) {
	t.Parallel()
	base := &ProbeOutcome{Trace: &ExecutionTrace{ProbeID: "base"}}
	cands := []ProbeOutcome{{Trace: &ExecutionTrace{ProbeID: "cand-1"}}}

	require.Equal(t, base, findOutcome("base", base, cands))
	require.Equal(t, "cand-1", findOutcome("cand-1", base, cands).Trace.ProbeID)
	require.Nil(t, findOutcome("nope", base, cands))
}
