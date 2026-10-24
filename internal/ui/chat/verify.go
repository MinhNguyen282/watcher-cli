package chat

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/verify"
)

// VerifyToolMessageItem renders project verification runs.
type VerifyToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*VerifyToolMessageItem)(nil)

// NewVerifyToolMessageItem creates a new [VerifyToolMessageItem].
func NewVerifyToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	base := newBaseToolMessageItem(sty, toolCall, result, &VerifyToolRenderContext{}, canceled)
	return &VerifyToolMessageItem{baseToolMessageItem: base}
}

// VerifyToolRenderContext renders verify tool messages.
type VerifyToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (v *VerifyToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	if opts.IsPending() {
		return pendingTool(sty, "Verify", opts.Anim, opts.Compact)
	}

	var params tools.VerifyParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	var headerParams []string
	if params.Full {
		headerParams = append(headerParams, "full", "true")
	}
	if len(params.Targets) > 0 {
		headerParams = append(headerParams, "targets", strings.Join(params.Targets, ", "))
	}
	header := toolHeader(sty, opts.Status, "Verify", cappedWidth, opts, headerParams...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
		return joinToolParts(header, earlyState)
	}
	if !opts.HasResult() || strings.TrimSpace(opts.Result.Content) == "" {
		return header
	}

	bodyWidth := cappedWidth - toolBodyLeftPaddingTotal
	report, parsed := verify.ParseReport(opts.Result.Content)
	if !parsed {
		body := sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, bodyWidth, opts.ExpandedContent))
		return joinToolParts(header, body)
	}

	body := sty.Tool.Body.Render(renderVerifyReport(report, opts.ExpandedContent))
	return joinToolParts(header, body)
}

// renderVerifyReport turns the parsed envelope into a structured card. Failed
// checks come first, and every state carries a glyph and a label so color is
// never the only cue.
func renderVerifyReport(report verify.Report, expanded bool) string {
	if len(report.Checks) == 0 && len(report.Failures) == 0 {
		return "No checks reported"
	}

	var b strings.Builder
	status := "Passed"
	if report.Status != "passed" {
		status = "Failed"
	}
	fmt.Fprintf(&b, "%s  %s  %s\n", status, report.Profile, report.Duration.Round(time.Millisecond))

	checks := slices.Clone(report.Checks)
	slices.SortStableFunc(checks, func(a, b verify.ReportCheck) int {
		if a.Failed == b.Failed {
			return 0
		}
		if a.Failed {
			return -1
		}
		return 1
	})
	for _, check := range checks {
		glyph, label := "ok", "passed"
		switch {
		case check.TimedOut:
			glyph, label = "!", "timed out"
		case check.Failed:
			glyph, label = "x", "failed"
		}
		fmt.Fprintf(&b, "  %s %s  %s (%s)\n", glyph, check.Name, label, check.Duration.Round(time.Millisecond))
		if expanded {
			fmt.Fprintf(&b, "      %s\n", check.Command)
		}
	}

	for _, failure := range report.Failures {
		location := failure.File
		if failure.Line > 0 {
			location = fmt.Sprintf("%s:%d", location, failure.Line)
		}
		if location == "" {
			location = "(unknown)"
		}
		if failure.Test != "" {
			location += " [" + failure.Test + "]"
		}
		fmt.Fprintf(&b, "  ! %s: %s\n", location, failure.Message)
	}

	if expanded && report.Output != "" {
		b.WriteString("\nOutput:\n")
		b.WriteString(report.Output)
	}
	return strings.TrimRight(b.String(), "\n")
}
