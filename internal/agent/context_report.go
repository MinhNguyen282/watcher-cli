package agent

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/session"
)

// tokenEstimatePerChar is the crude but stable conversion used for prompts
// and schemas, where we only have text lengths. Four characters per token
// matches English prose closely enough to spot a blown budget; the provider
// usage numbers remain authoritative for what was actually billed.
const tokenEstimatePerChar = 4

// contextReportTopTools is how many of the largest tool schemas the report
// lists.
const contextReportTopTools = 15

// estimateTokens converts a byte count to a rough token count.
func estimateTokens(chars int) int {
	if chars <= 0 {
		return 0
	}
	return chars / tokenEstimatePerChar
}

// buildContextReport renders the /context-equivalent accounting for one
// session: the window, the prompt the provider last processed, the prompt
// and schema estimates, and the deferral state.
func buildContextReport(
	window int64,
	sessionID string,
	sessions session.Service,
	systemPrompt string,
	toolList []fantasy.AgentTool,
	deferred *tools.ToolSearchState,
) string {
	var b strings.Builder
	b.WriteString("[context]\n")
	fmt.Fprintf(&b, "window = %d tokens\n", window)

	var promptTokens int64
	if sessions != nil && sessionID != "" {
		if sess, err := sessions.Get(context.Background(), sessionID); err == nil {
			promptTokens = sess.PromptTokens
			fmt.Fprintf(&b, "prompt_tokens_last_step = %d\n", sess.PromptTokens)
			fmt.Fprintf(&b, "completion_tokens = %d\n", sess.CompletionTokens)
			if sess.EstimatedUsage {
				b.WriteString("usage_estimated = true\n")
			}
		}
	}
	if window > 0 {
		fmt.Fprintf(&b, "estimated_remaining = %d\n", window-promptTokens)
	}
	fmt.Fprintf(&b, "system_prompt_chars = %d\n", len(systemPrompt))
	fmt.Fprintf(&b, "system_prompt_tokens_est = %d\n", estimateTokens(len(systemPrompt)))
	b.WriteString("\n")

	type toolRow struct {
		name   string
		chars  int
		tokens int
		state  string
	}
	rows := make([]toolRow, 0, len(toolList))
	active, loaded, deferredCount := 0, 0, 0
	for _, tool := range toolList {
		name := tool.Info().Name
		state := "active"
		switch {
		case deferred.IsPromoted(name):
			state = "loaded"
			loaded++
		case tools.IsDeferrable(name):
			state = "deferred"
			deferredCount++
		default:
			active++
		}
		chars := len(name) + len(tool.Info().Description)
		rows = append(rows, toolRow{name: name, chars: chars, tokens: estimateTokens(chars), state: state})
	}

	totalChars := 0
	for _, row := range rows {
		if row.state == "deferred" {
			continue
		}
		totalChars += row.chars
	}

	b.WriteString("[tools]\n")
	fmt.Fprintf(&b, "total = %d\n", len(rows))
	fmt.Fprintf(&b, "active = %d\n", active)
	fmt.Fprintf(&b, "loaded = %d\n", loaded)
	fmt.Fprintf(&b, "deferred = %d\n", deferredCount)
	fmt.Fprintf(&b, "schema_tokens_est = %d\n", estimateTokens(totalChars))
	if len(rows) > 0 {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].chars != rows[j].chars {
				return rows[i].chars > rows[j].chars
			}
			return rows[i].name < rows[j].name
		})
		b.WriteString("\n[largest_schemas]\n")
		limit := min(len(rows), contextReportTopTools)
		for _, row := range rows[:limit] {
			fmt.Fprintf(&b, "%s = %d chars, ~%d tokens, %s\n", row.name, row.chars, row.tokens, row.state)
		}
	}

	if promoted := deferred.Promoted(); len(promoted) > 0 {
		slices.Sort(promoted)
		b.WriteString("\n[loaded_tools]\n")
		b.WriteString(strings.Join(promoted, "\n"))
		b.WriteString("\n")
	}
	return b.String()
}
