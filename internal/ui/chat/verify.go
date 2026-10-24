package chat

import (
	"encoding/json"
	"strings"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
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
	body := sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, bodyWidth, opts.ExpandedContent))
	return joinToolParts(header, body)
}
