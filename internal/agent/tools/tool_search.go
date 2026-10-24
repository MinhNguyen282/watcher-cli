package tools

import (
	"context"
	_ "embed"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"charm.land/fantasy"
)

//go:embed tool_search.md
var toolSearchDescription string

// ToolSearchToolName is the tool that loads deferred tool schemas.
const ToolSearchToolName = "tool_search"

// maxToolSearchResults bounds how many tools one search can load. Loading
// every deferred schema at once defeats the point of deferring them.
const maxToolSearchResults = 5

// ToolSearchState tracks which deferred tools a session has loaded. A tool
// stays loaded for the session once searched, so the model does not have to
// search again for every call.
type ToolSearchState struct {
	mu       sync.RWMutex
	promoted map[string]bool
}

// NewToolSearchState returns empty search state.
func NewToolSearchState() *ToolSearchState {
	return &ToolSearchState{promoted: make(map[string]bool)}
}

// IsPromoted reports whether a deferred tool has been loaded.
func (s *ToolSearchState) IsPromoted(name string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.promoted[name]
}

// Promote marks a tool as loaded and reports whether it changed.
func (s *ToolSearchState) Promote(name string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.promoted[name] {
		return false
	}
	s.promoted[name] = true
	return true
}

// Promoted returns the loaded tool names, sorted.
func (s *ToolSearchState) Promoted() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.promoted))
	for name := range s.promoted {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// IsDeferrable reports whether a tool's schema may be withheld until
// searched. Declared specs decide; tools without a spec (MCP servers,
// plugins) are deferrable because their schemas are usually the largest and
// the least frequently used.
func IsDeferrable(name string) bool {
	spec, declared := SpecFor(name)
	if !declared {
		return true
	}
	return spec.Deferrable
}

// toolSearchStateKey carries the session's search state in a context so the
// tool does not need the coordinator.
type toolSearchStateKey struct{}

// WithToolSearchState attaches the search state to a context.
func WithToolSearchState(ctx context.Context, state *ToolSearchState) context.Context {
	return context.WithValue(ctx, toolSearchStateKey{}, state)
}

func toolSearchStateFrom(ctx context.Context) *ToolSearchState {
	state, _ := ctx.Value(toolSearchStateKey{}).(*ToolSearchState)
	return state
}

type toolSearchParams struct {
	Query string `json:"query" description:"What capability you need, in words (for example \"list github pull requests\")"`
}

// NewToolSearchTool builds the tool that reveals deferred schemas. The
// catalog is the full tool list; only deferred tools can be promoted.
func NewToolSearchTool(catalog []fantasy.AgentTool) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		ToolSearchToolName,
		toolSearchDescription,
		func(ctx context.Context, params toolSearchParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			state := toolSearchStateFrom(ctx)
			if state == nil {
				return fantasy.NewTextErrorResponse("Tool search is not active in this session."), nil
			}
			query := strings.TrimSpace(params.Query)
			if query == "" {
				return fantasy.NewTextErrorResponse("query is required"), nil
			}

			matches := searchTools(catalog, query, maxToolSearchResults)
			if len(matches) == 0 {
				return fantasy.NewTextResponse(fmt.Sprintf(
					"No tool matches %q. Try different words, or use the tool directly if you already know its name.", query,
				)), nil
			}

			var b strings.Builder
			b.WriteString("Loaded tool schemas for this session:\n")
			for _, tool := range matches {
				state.Promote(tool.Info().Name)
				fmt.Fprintf(&b, "- %s: %s\n", tool.Info().Name, firstLine(tool.Info().Description))
			}
			b.WriteString("\nCall them directly from now on.")
			return fantasy.NewTextResponse(b.String()), nil
		},
	)
}

// searchTools ranks deferred tools by a case-insensitive substring match on
// name, search hint and description.
func searchTools(catalog []fantasy.AgentTool, query string, limit int) []fantasy.AgentTool {
	needle := strings.ToLower(query)
	terms := make([]string, 0, 4)
	for _, term := range strings.Fields(needle) {
		if len(term) >= 3 {
			terms = append(terms, term)
		}
	}
	if len(terms) == 0 {
		terms = []string{needle}
	}

	type scored struct {
		tool  fantasy.AgentTool
		score int
	}
	var results []scored
	for _, tool := range catalog {
		name := tool.Info().Name
		if !IsDeferrable(name) {
			continue
		}
		spec, _ := SpecFor(name)
		haystack := strings.ToLower(name + " " + spec.SearchHint + " " + tool.Info().Description)
		score := 0
		if strings.Contains(haystack, needle) {
			score += 10
		}
		for _, term := range terms {
			if strings.Contains(haystack, term) {
				score += 2
			}
		}
		if strings.Contains(strings.ToLower(name), needle) {
			score += 5
		}
		if score > 0 {
			results = append(results, scored{tool: tool, score: score})
		}
	}

	slices.SortStableFunc(results, func(a, b scored) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return strings.Compare(a.tool.Info().Name, b.tool.Info().Name)
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	out := make([]fantasy.AgentTool, 0, len(results))
	for _, result := range results {
		out = append(out, result.tool)
	}
	return out
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return line
}
