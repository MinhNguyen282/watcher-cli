package tools

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

type fakeTool struct {
	name string
	desc string
}

func (f *fakeTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: f.name, Description: f.desc}
}

func (f *fakeTool) Run(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.NewTextResponse("ok"), nil
}

func (f *fakeTool) ProviderOptions() fantasy.ProviderOptions   { return nil }
func (f *fakeTool) SetProviderOptions(fantasy.ProviderOptions) {}

func TestIsDeferrable(t *testing.T) {
	t.Parallel()

	require.True(t, IsDeferrable("mcp__github__list_prs"), "undeclared tools are deferrable")
	require.False(t, IsDeferrable(ViewToolName), "declared tools are not deferrable")
}

func TestToolSearchStatePromotesOnce(t *testing.T) {
	t.Parallel()

	state := NewToolSearchState()
	require.False(t, state.IsPromoted("mcp__x"))
	require.True(t, state.Promote("mcp__x"))
	require.False(t, state.Promote("mcp__x"))
	require.True(t, state.IsPromoted("mcp__x"))
	require.Equal(t, []string{"mcp__x"}, state.Promoted())
}

func TestToolSearchPromotesMatchingDeferredTools(t *testing.T) {
	t.Parallel()

	catalog := []fantasy.AgentTool{
		&fakeTool{name: "mcp__github__list_pull_requests", desc: "List pull requests in a repository"},
		&fakeTool{name: "mcp__linear__create_issue", desc: "Create an issue in a Linear team"},
		&fakeTool{name: ViewToolName, desc: "Read a file"},
	}
	state := NewToolSearchState()
	tool := NewToolSearchTool(catalog)

	ctx := WithToolSearchState(context.Background(), state)
	response, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "search-1",
		Name:  ToolSearchToolName,
		Input: `{"query":"pull requests"}`,
	})
	require.NoError(t, err)
	require.False(t, response.IsError, response.Content)
	require.Contains(t, response.Content, "mcp__github__list_pull_requests")
	require.NotContains(t, response.Content, "mcp__linear__create_issue")
	require.True(t, state.IsPromoted("mcp__github__list_pull_requests"))

	// A declared tool is never a search result; it is already available.
	response, err = tool.Run(ctx, fantasy.ToolCall{Name: ToolSearchToolName, Input: `{"query":"read a file"}`})
	require.NoError(t, err)
	require.Contains(t, response.Content, "No tool matches")
}

func TestToolSearchWithoutState(t *testing.T) {
	t.Parallel()

	tool := NewToolSearchTool(nil)
	response, err := tool.Run(context.Background(), fantasy.ToolCall{Name: ToolSearchToolName, Input: `{"query":"x"}`})
	require.NoError(t, err)
	require.True(t, response.IsError)
	require.Contains(t, response.Content, "not active")
}
