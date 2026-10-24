package tools

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/memory"
)

// Memory tool names.
const (
	MemorySaveToolName   = "memory_save"
	MemorySearchToolName = "memory_search"
	MemoryForgetToolName = "memory_forget"
)

//go:embed memory_save.md
var memorySaveDescription string

//go:embed memory_search.md
var memorySearchDescription string

//go:embed memory_forget.md
var memoryForgetDescription string

// MemorySaveParams is the input for memory_save.
type MemorySaveParams struct {
	ID      string `json:"id,omitempty" description:"Existing entry id to update; omit to create a new entry"`
	Type    string `json:"type" description:"Memory scope: user, project or team"`
	Title   string `json:"title" description:"Short title for the memory"`
	Content string `json:"content" description:"The memory itself, with the detail that would be missed later"`
}

// MemorySearchParams is the input for memory_search.
type MemorySearchParams struct {
	Query string `json:"query" description:"Words describing what you are looking for"`
	Limit int    `json:"limit,omitempty" description:"Maximum entries to return (default 5)"`
}

// MemoryForgetParams is the input for memory_forget.
type MemoryForgetParams struct {
	ID string `json:"id" description:"Entry id to remove"`
}

// NewMemorySaveTool builds the write half of the memory tools.
func NewMemorySaveTool(store *memory.Store) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		MemorySaveToolName,
		memorySaveDescription,
		func(_ context.Context, params MemorySaveParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if store == nil {
				return fantasy.NewTextErrorResponse("Memory is unavailable in this session."), nil
			}
			entry, err := store.Save(memory.Entry{
				ID:      strings.TrimSpace(params.ID),
				Type:    memory.Type(strings.TrimSpace(params.Type)),
				Title:   params.Title,
				Content: params.Content,
			})
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			return fantasy.NewTextResponse(fmt.Sprintf(
				"Saved %s memory %q (id %s).", entry.Type, entry.Title, entry.ID,
			)), nil
		},
	)
}

// NewMemorySearchTool builds the read half of the memory tools.
func NewMemorySearchTool(store *memory.Store) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		MemorySearchToolName,
		memorySearchDescription,
		func(_ context.Context, params MemorySearchParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if store == nil {
				return fantasy.NewTextErrorResponse("Memory is unavailable in this session."), nil
			}
			query := strings.TrimSpace(params.Query)
			if query == "" {
				return fantasy.NewTextErrorResponse("query is required"), nil
			}
			matches := store.Search(query, params.Limit)
			if len(matches) == 0 {
				return fantasy.NewTextResponse(fmt.Sprintf("No memories match %q.", query)), nil
			}
			var b strings.Builder
			for _, entry := range matches {
				fmt.Fprintf(&b, "[%s] %s (id %s)\n%s\n\n", entry.Type, entry.Title, entry.ID, entry.Content)
			}
			return fantasy.NewTextResponse(strings.TrimRight(b.String(), "\n")), nil
		},
	)
}

// NewMemoryForgetTool builds the deletion half of the memory tools.
func NewMemoryForgetTool(store *memory.Store) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		MemoryForgetToolName,
		memoryForgetDescription,
		func(_ context.Context, params MemoryForgetParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if store == nil {
				return fantasy.NewTextErrorResponse("Memory is unavailable in this session."), nil
			}
			id := strings.TrimSpace(params.ID)
			if id == "" {
				return fantasy.NewTextErrorResponse("id is required"), nil
			}
			err := store.Forget(id)
			switch {
			case err == nil:
				return fantasy.NewTextResponse("Forgot memory " + id + "."), nil
			case errors.Is(err, memory.ErrNotFound):
				return fantasy.NewTextErrorResponse("No memory entry with id " + id + "; search first."), nil
			default:
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
		},
	)
}
