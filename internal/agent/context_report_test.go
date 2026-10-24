package agent

import (
	"context"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/stretchr/testify/require"
)

type reportTool struct {
	name string
	desc string
}

func (r *reportTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: r.name, Description: r.desc}
}

func (r *reportTool) Run(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.NewTextResponse("ok"), nil
}

func (r *reportTool) ProviderOptions() fantasy.ProviderOptions   { return nil }
func (r *reportTool) SetProviderOptions(fantasy.ProviderOptions) {}

func TestEstimateTokens(t *testing.T) {
	t.Parallel()

	require.Zero(t, estimateTokens(0))
	require.Zero(t, estimateTokens(-4))
	require.Equal(t, 25, estimateTokens(100))
}

func TestBuildContextReport(t *testing.T) {
	t.Parallel()

	catalog := []fantasy.AgentTool{
		&reportTool{name: "grep", desc: strings.Repeat("x", 400)},
		&reportTool{name: "mcp__github__list_prs", desc: strings.Repeat("y", 800)},
		&reportTool{name: "mcp__linear__create_issue", desc: strings.Repeat("z", 100)},
	}
	deferred := tools.NewToolSearchState()
	deferred.Promote("mcp__linear__create_issue")

	report := buildContextReport(200_000, "", nil, strings.Repeat("p", 2000), catalog, deferred)

	require.Contains(t, report, "[context]")
	require.Contains(t, report, "window = 200000 tokens")
	require.Contains(t, report, "system_prompt_tokens_est = 500")

	require.Contains(t, report, "[tools]")
	require.Contains(t, report, "total = 3")
	require.Contains(t, report, "active = 1", "only grep is active; loaded tools form their own bucket")
	require.Contains(t, report, "loaded = 1")
	require.Contains(t, report, "deferred = 1")

	// Deferred schemas are not counted toward what the model is paying for.
	require.Contains(t, report, "schema_tokens_est = 132")

	require.Contains(t, report, "[largest_schemas]")
	require.Contains(t, report, "mcp__github__list_prs = 821 chars, ~205 tokens, deferred")

	require.Contains(t, report, "[loaded_tools]")
	require.Contains(t, report, "mcp__linear__create_issue")
}

func TestBuildContextReportEmptyCatalog(t *testing.T) {
	t.Parallel()

	report := buildContextReport(0, "", nil, "", nil, nil)
	require.Contains(t, report, "window = 0 tokens")
	require.Contains(t, report, "total = 0")
	require.NotContains(t, report, "[largest_schemas]")
	require.NotContains(t, report, "[loaded_tools]")
}

func TestReadOnlyToolNames(t *testing.T) {
	t.Parallel()

	got := readOnlyToolNames([]string{"grep", "edit", "bash", "view", "memory_search", "write"})
	require.ElementsMatch(t, []string{"grep", "view", "memory_search"}, got)
}
