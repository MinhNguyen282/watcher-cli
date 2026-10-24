package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func testRun(id, name, parent string) *subagentRun {
	return &subagentRun{
		info: SubagentInfo{
			ID:              id,
			Name:            name,
			Type:            SubagentTask,
			SessionID:       "child-" + id,
			ParentSessionID: parent,
			State:           SubagentCompleted,
		},
	}
}

func TestSubagentRegistryFind(t *testing.T) {
	t.Parallel()

	registry := newSubagentRegistry()
	first := testRun("alpha-1", "researcher", "parent")
	second := testRun("alpha-2", "reviewer", "parent")
	third := testRun("beta-2", "writer", "parent")
	registry.add(first)
	registry.add(second)
	registry.add(third)

	run, ok := registry.find("alpha-1")
	require.True(t, ok)
	require.Same(t, first, run)

	// Names are matched case-insensitively.
	run, ok = registry.find("Writer")
	require.True(t, ok)
	require.Same(t, third, run)

	// A unique id prefix resolves.
	run, ok = registry.find("bet")
	require.True(t, ok)
	require.Same(t, third, run)

	// An ambiguous prefix does not guess.
	_, ok = registry.find("alpha")
	require.False(t, ok)
	_, ok = registry.find("nobody")
	require.False(t, ok)
	_, ok = registry.find("")
	require.False(t, ok)
}

func TestSubagentRunSettlesOnceAndReleasesWaiters(t *testing.T) {
	t.Parallel()

	run := testRun("subagent-1", "", "parent")
	done := run.beginTurn()
	registry := newSubagentRegistry()
	registry.add(run)
	require.Equal(t, SubagentRunning, run.snapshot().State)
	require.Equal(t, 1, registry.running())

	run.settle(SubagentCompleted, "report", "")
	select {
	case <-done:
	default:
		require.Fail(t, "settle must release waiters")
	}
	info := run.snapshot()
	require.Equal(t, SubagentCompleted, info.State)
	require.Equal(t, "report", info.Output)

	// A second settle must not panic on the closed channel or overwrite
	// the terminal state.
	run.settle(SubagentFailed, "", "late")
	require.Equal(t, SubagentCompleted, run.snapshot().State)
	require.NoError(t, run.wait(context.Background()))
	require.Zero(t, registry.running())
}

func TestDescribeSubagentTruncatesOutput(t *testing.T) {
	t.Parallel()

	info := SubagentInfo{
		ID:        "subagent-1",
		Name:      "researcher",
		Type:      SubagentTask,
		SessionID: "child",
		State:     SubagentCompleted,
		Output:    strings.Repeat("x", subagentOutputLimit+10),
	}
	out := describeSubagent(info)
	require.Contains(t, out, "subagent-1 (researcher)")
	require.Contains(t, out, "state: completed")
	require.Contains(t, out, "[output truncated")
}

func TestTakePrecomputedSummarySplitsTranscript(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{precompute: map[string]*summaryPrecompute{
		"s1": {
			lastMessageID: "m2",
			text:          "earlier summary",
			usage:         fantasy.Usage{TotalTokens: 42},
			created:       time.Now(),
		},
	}}
	msgs := []message.Message{{ID: "m1"}, {ID: "m2"}, {ID: "m3"}}

	text, remainder, usage, ok := agent.takePrecomputedSummary("s1", msgs)
	require.True(t, ok)
	require.Equal(t, "earlier summary", text)
	require.Equal(t, []message.Message{{ID: "m3"}}, remainder)
	require.Equal(t, int64(42), usage.TotalTokens)

	// The cache is single-use.
	_, _, _, ok = agent.takePrecomputedSummary("s1", msgs)
	require.False(t, ok)
}

func TestTakePrecomputedSummaryRejectsStaleAndUnknownAnchors(t *testing.T) {
	t.Parallel()

	agent := &sessionAgent{precompute: map[string]*summaryPrecompute{
		"stale": {
			lastMessageID: "m1",
			text:          "old",
			created:       time.Now().Add(-precomputeSummaryTTL - time.Minute),
		},
		"anchor": {
			lastMessageID: "missing",
			text:          "unusable",
			created:       time.Now(),
		},
	}}
	msgs := []message.Message{{ID: "m1"}}

	_, _, _, ok := agent.takePrecomputedSummary("stale", msgs)
	require.False(t, ok)
	_, _, _, ok = agent.takePrecomputedSummary("anchor", msgs)
	require.False(t, ok)
	_, _, _, ok = agent.takePrecomputedSummary("absent", msgs)
	require.False(t, ok)
}
