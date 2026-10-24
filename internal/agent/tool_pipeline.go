package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/tidwall/sjson"
)

const (
	// defaultToolTimeout bounds a single tool body. Tools that manage their
	// own lifetime, such as bash and job_output, override this.
	defaultToolTimeout = 10 * time.Minute
)

// ToolGuard may deny a call. An empty reason abstains; any non-empty reason
// denies. Guards are evaluated in order and a denial is final, so a later
// guard or listener cannot turn it back into an allow.
type ToolGuard func(ctx context.Context, call fantasy.ToolCall) string

// ToolEventRecorder persists tool-call intent and its result. Implementations
// must be append-only; the pipeline calls them once per call and once per
// outcome, including denials and timeouts.
type ToolEventRecorder interface {
	RecordToolCallIntent(ctx context.Context, call fantasy.ToolCall)
	RecordToolResult(ctx context.Context, call fantasy.ToolCall, response fantasy.ToolResponse, outcome string)
}

// toolPipeline is the single dispatch path for every model tool call. It
// resolves hooks and input rewrites first, logs the final intent, applies
// monotonic guards, injects the approval stage, executes with a timeout,
// and normalizes every outcome into exactly one tool response.
type toolPipeline struct {
	inner       fantasy.AgentTool
	permissions permission.Service
	runner      *hooks.Runner
	guards      []ToolGuard
	recorder    ToolEventRecorder
	timeout     time.Duration
}

type pipelineResult struct {
	response fantasy.ToolResponse
	err      error
	panicVal any
}

type toolOutcomeMetadata struct {
	Outcome    string `json:"outcome"`
	Reason     string `json:"reason,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Aborted    bool   `json:"aborted,omitempty"`
}

func withToolOutcomeMetadata(response fantasy.ToolResponse, meta toolOutcomeMetadata) fantasy.ToolResponse {
	data, err := json.Marshal(meta)
	if err != nil {
		return response
	}
	response.Metadata = `{"tool_outcome":` + string(data) + `}`
	return response
}

// wrapToolsWithPipeline replaces the old hook decorator with the unified
// pipeline. Sub-agents skip hooks, matching the previous behavior, but still
// go through approval, guards, timeout, and normalization.
func wrapToolsWithPipeline(ts []fantasy.AgentTool, permissions permission.Service, runner *hooks.Runner, isSubAgent bool, guards ...ToolGuard) []fantasy.AgentTool {
	out := make([]fantasy.AgentTool, 0, len(ts))
	for _, tool := range ts {
		var hookRunner *hooks.Runner
		if !isSubAgent {
			hookRunner = runner
		}
		out = append(out, &toolPipeline{
			inner:       tool,
			permissions: permissions,
			runner:      hookRunner,
			guards:      guards,
			timeout:     toolTimeout(tool.Info().Name),
		})
	}
	return out
}

// withToolRecorder attaches a durable recorder to every pipeline in the
// slice. It is used by the coordinator after the session service exists.
func withToolRecorder(ts []fantasy.AgentTool, recorder ToolEventRecorder) []fantasy.AgentTool {
	out := make([]fantasy.AgentTool, 0, len(ts))
	for _, tool := range ts {
		if pipeline, ok := tool.(*toolPipeline); ok {
			pipeline.recorder = recorder
		}
		out = append(out, tool)
	}
	return out
}

// toolTimeout returns the per-call budget. Zero disables the wrapper for
// tools whose own loop already bounds waiting.
func toolTimeout(name string) time.Duration {
	switch name {
	case tools.BashToolName, tools.JobOutputToolName, tools.QuestionToolName:
		return 0
	case tools.VerifyToolName:
		return 15 * time.Minute
	default:
		return defaultToolTimeout
	}
}

func (p *toolPipeline) Info() fantasy.ToolInfo {
	return p.inner.Info()
}

func (p *toolPipeline) ProviderOptions() fantasy.ProviderOptions {
	return p.inner.ProviderOptions()
}

func (p *toolPipeline) SetProviderOptions(opts fantasy.ProviderOptions) {
	p.inner.SetProviderOptions(opts)
}

func (p *toolPipeline) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	started := time.Now()
	sessionID := tools.GetSessionFromContext(ctx)

	// Hooks resolve before the intent record so no logged argument is ever
	// rewritten afterwards.
	if p.runner != nil {
		result, err := p.runner.Run(ctx, hooks.EventPreToolUse, sessionID, call.Name, call.Input)
		if err != nil {
			slog.Warn("Hook execution error, proceeding with tool call",
				"tool", call.Name, "error", err)
		}
		if result.Decision == hooks.DecisionDeny || result.Halt {
			reason := fmt.Sprintf("Tool call blocked by hook. Reason: %s", result.Reason)
			if result.Halt {
				reason = fmt.Sprintf("Turn halted by hook. Reason: %s", result.Reason)
			}
			resp := fantasy.NewTextErrorResponse(reason)
			resp.StopTurn = result.Halt
			resp.Metadata = hookMetadataJSON(result)
			if p.recorder != nil {
				p.recorder.RecordToolCallIntent(ctx, call)
				p.recorder.RecordToolResult(ctx, call, resp, "denied_by_hook")
			}
			return resp, nil
		}
		if result.UpdatedInput != "" {
			call.Input = result.UpdatedInput
		}
		ctx = context.WithValue(ctx, hookResultKey{}, result)
		if result.Decision == hooks.DecisionAllow {
			ctx = permission.WithHookApproval(ctx, call.ID)
		}
	}

	// Model-visible means logged: record the final intent before dispatch.
	slog.Debug("Tool call starting",
		"tool", call.Name, "tool_call_id", call.ID, "session_id", sessionID,
		"input", call.Input)
	if p.recorder != nil {
		p.recorder.RecordToolCallIntent(ctx, call)
	}

	for _, guard := range p.guards {
		if guard == nil {
			continue
		}
		if reason := guard(ctx, call); reason != "" {
			resp := withToolOutcomeMetadata(
				fantasy.NewTextErrorResponse("Tool call denied by guard: "+reason),
				toolOutcomeMetadata{Outcome: "denied", Reason: reason, DurationMS: time.Since(started).Milliseconds()},
			)
			p.recordResult(ctx, call, resp, "denied")
			return resp, nil
		}
	}

	if reason := p.modeGuard(call); reason != "" {
		resp := withToolOutcomeMetadata(
			fantasy.NewTextErrorResponse("Tool call denied: "+reason),
			toolOutcomeMetadata{Outcome: "denied", Reason: reason, DurationMS: time.Since(started).Milliseconds()},
		)
		p.recordResult(ctx, call, resp, "denied")
		return resp, nil
	}

	ctx = tools.WithApprover(ctx, p.approver(call.Input))

	response, outcome := p.execute(ctx, call)
	response = p.emitToolOutcome(ctx, call, response, outcome)
	response = p.attachPermissionStage(ctx, call, response)
	response = p.finalize(ctx, call, response, outcome)
	p.recordResult(ctx, call, response, outcome)
	slog.Debug("Tool call finished",
		"tool", call.Name, "tool_call_id", call.ID, "session_id", sessionID,
		"outcome", outcome, "duration_ms", time.Since(started).Milliseconds())
	return response, nil
}

// modeGuard enforces mode-level restrictions that must hold even for tools
// that never request approval. Plan mode is read-only, so a mutating tool is
// refused here instead of relying on the tool to ask.
func (p *toolPipeline) modeGuard(call fantasy.ToolCall) string {
	modeService, ok := p.permissions.(permission.ModeService)
	if !ok {
		return ""
	}
	if modeService.Mode() != permission.ModePlan {
		return ""
	}
	if !tools.IsMutating(call.Name) {
		return ""
	}
	return "plan mode is read-only, and " + call.Name + " can change state"
}

// approver binds the current call's input to the approval stage so rule
// content is derived from what the model actually sent.
func (p *toolPipeline) approver(input string) tools.Approver {
	return func(ctx context.Context, req permission.CreatePermissionRequest) (tools.ApprovalOutcome, error) {
		spec, _ := tools.SpecFor(req.ToolName)
		req.Mutating = spec.Mutating
		req.ReadOnly = spec.ReadOnly
		if req.RuleContent == "" {
			req.RuleContent = tools.RuleContentFor(req.ToolName, input)
		}

		// PermissionRequest runs only for calls that would prompt. A hook
		// allow is stamped as a hook approval so the permission service
		// records the provenance; a hook deny skips the prompt entirely.
		if p.runner != nil && p.runner.HasEvent(hooks.EventPermissionRequest) {
			result, err := p.runner.Run(ctx, hooks.EventPermissionRequest, tools.GetSessionFromContext(ctx), req.ToolName, input)
			if err != nil {
				slog.Warn("PermissionRequest hook failed, continuing", "tool", req.ToolName, "error", err)
			}
			switch {
			case result.Decision == hooks.DecisionDeny || result.Halt:
				p.emitPermissionDenied(ctx, req, "denied by hook: "+result.Reason)
				return tools.ApprovalRejected, nil
			case result.Decision == hooks.DecisionAllow:
				ctx = permission.WithHookApproval(ctx, req.ToolCallID)
			}
		}

		outcome, err := p.approve(ctx, req)
		if outcome == tools.ApprovalRejected {
			p.emitPermissionDenied(ctx, req, "")
		}
		return outcome, err
	}
}

// approve is the pipeline's approval stage. It maps the permission service's
// boolean answer onto the closed outcome set; an absent service or an error
// fails closed.
func (p *toolPipeline) approve(ctx context.Context, req permission.CreatePermissionRequest) (tools.ApprovalOutcome, error) {
	if p.permissions == nil {
		return tools.ApprovalUnavailable, errors.New("permission service unavailable; denying tool call")
	}
	granted, err := p.permissions.Request(ctx, req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			slog.Info("Tool approval cancelled",
				"tool", req.ToolName, "tool_call_id", req.ToolCallID)
			return tools.ApprovalCancelled, fmt.Errorf("approval cancelled: %w", err)
		}
		slog.Warn("Tool approval failed closed",
			"tool", req.ToolName, "tool_call_id", req.ToolCallID, "error", err)
		return tools.ApprovalUnavailable, fmt.Errorf("approval failed: %w", err)
	}
	if !granted {
		slog.Info("Tool approval rejected",
			"tool", req.ToolName, "tool_call_id", req.ToolCallID)
		return tools.ApprovalRejected, nil
	}
	slog.Debug("Tool approval granted",
		"tool", req.ToolName, "tool_call_id", req.ToolCallID)
	return tools.ApprovalAllowedOnce, nil
}

// execute runs the body with a timeout and converts panics into results.
func (p *toolPipeline) execute(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, string) {
	if p.timeout <= 0 {
		result := runToolBody(ctx, p.inner, call)
		return p.resultOutcome(result, false, ctx.Err() != nil, ctx.Err())
	}

	runCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	ch := make(chan pipelineResult, 1)
	go func() {
		ch <- runToolBody(runCtx, p.inner, call)
	}()

	select {
	case result := <-ch:
		return p.resultOutcome(result, false, ctx.Err() != nil, ctx.Err())
	case <-runCtx.Done():
		if ctx.Err() != nil {
			resp := withToolOutcomeMetadata(
				fantasy.NewTextErrorResponse("Tool call aborted: "+ctx.Err().Error()),
				toolOutcomeMetadata{Outcome: "aborted", Aborted: true, Reason: ctx.Err().Error()},
			)
			resp.StopTurn = true
			return resp, "aborted"
		}
		return withToolOutcomeMetadata(
			fantasy.NewTextErrorResponse(fmt.Sprintf("Tool call timed out after %s", p.timeout)),
			toolOutcomeMetadata{Outcome: "timed_out", TimedOut: true, Reason: p.timeout.String()},
		), "timed_out"
	}
}

func (p *toolPipeline) resultOutcome(result pipelineResult, timedOut, aborted bool, ctxErr error) (fantasy.ToolResponse, string) {
	if result.panicVal != nil {
		return withToolOutcomeMetadata(
			fantasy.NewTextErrorResponse(fmt.Sprintf("Tool panicked: %v", result.panicVal)),
			toolOutcomeMetadata{Outcome: "panic", Reason: fmt.Sprint(result.panicVal)},
		), "panic"
	}
	if result.err != nil {
		switch {
		case aborted:
			return withToolOutcomeMetadata(
				fantasy.NewTextErrorResponse("Tool call aborted: "+result.err.Error()),
				toolOutcomeMetadata{Outcome: "aborted", Aborted: true, Reason: result.err.Error()},
			), "aborted"
		case timedOut || errors.Is(result.err, context.DeadlineExceeded) || errors.Is(ctxErr, context.DeadlineExceeded):
			return withToolOutcomeMetadata(
				fantasy.NewTextErrorResponse("Tool call timed out: "+result.err.Error()),
				toolOutcomeMetadata{Outcome: "timed_out", TimedOut: true, Reason: result.err.Error()},
			), "timed_out"
		default:
			return withToolOutcomeMetadata(
				fantasy.NewTextErrorResponse(result.err.Error()),
				toolOutcomeMetadata{Outcome: "error", Reason: result.err.Error()},
			), "error"
		}
	}
	return result.response, "completed"
}

func runToolBody(ctx context.Context, inner fantasy.AgentTool, call fantasy.ToolCall) (result pipelineResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = pipelineResult{panicVal: recovered}
		}
	}()
	result.response, result.err = inner.Run(ctx, call)
	return result
}

// finalize guarantees one structurally complete result. Hook-provided context
// is appended after normalization so a hook cannot turn an error into a
// success or hide the failure.
func (p *toolPipeline) finalize(ctx context.Context, call fantasy.ToolCall, response fantasy.ToolResponse, outcome string) fantasy.ToolResponse {
	if hookResult, ok := ctx.Value(hookResultKey{}).(hooks.AggregateResult); ok {
		if hookResult.Context != "" {
			if response.Content != "" {
				response.Content += "\n"
			}
			response.Content += hookResult.Context
		}
		if hookResult.HookCount > 0 {
			response.Metadata = mergeHookMetadata(response.Metadata, hookResult)
		}
	}
	if outcome == "completed" && response.Content == "" && len(response.Data) == 0 {
		response.Content = "Tool completed without output"
	}
	// The harness owns the result ceiling, not the individual tools: a tool
	// that forgets to bound its output still cannot flood the context.
	if truncated, didTruncate := tools.TruncateResult(call.Name, response); didTruncate {
		response = truncated
		spec, _ := tools.SpecFor(call.Name)
		if updated, err := sjson.Set(response.Metadata, "result_truncated", true); err == nil {
			response.Metadata = updated
		}
		if updated, err := sjson.Set(response.Metadata, "result_limit_chars", spec.ResultLimit()); err == nil {
			response.Metadata = updated
		}
	}
	return response
}

// attachPermissionStage records who decided the call, so a denial can be
// explained in the UI, the log and the tool result itself.
func (p *toolPipeline) attachPermissionStage(ctx context.Context, call fantasy.ToolCall, response fantasy.ToolResponse) fantasy.ToolResponse {
	modeService, ok := p.permissions.(permission.ModeService)
	if !ok {
		return response
	}
	decision, ok := modeService.DecisionFor(call.ID)
	if !ok {
		return response
	}
	if updated, err := sjson.Set(response.Metadata, "permission_stage", string(decision.Stage)); err == nil {
		response.Metadata = updated
	}
	if decision.Reason != "" {
		if updated, err := sjson.Set(response.Metadata, "permission_reason", decision.Reason); err == nil {
			response.Metadata = updated
		}
	}
	return response
}

// emitToolOutcome runs the post-tool hooks and folds their context into the
// result. Post hooks are observational: they cannot turn a failure into a
// success, so the content is only appended to what the tool returned.
func (p *toolPipeline) emitToolOutcome(ctx context.Context, call fantasy.ToolCall, response fantasy.ToolResponse, outcome string) fantasy.ToolResponse {
	if p.runner == nil {
		return response
	}
	event := hooks.EventPostToolUse
	if outcome != "completed" {
		event = hooks.EventPostToolUseFailure
	}
	if !p.runner.HasEvent(event) {
		return response
	}
	result, err := p.runner.Run(ctx, event, tools.GetSessionFromContext(ctx), call.Name, call.Input)
	if err != nil {
		slog.Warn("Post-tool hook failed", "event", event, "tool", call.Name, "error", err)
		return response
	}
	if result.Context != "" {
		if response.Content != "" {
			response.Content += "\n"
		}
		response.Content += result.Context
	}
	if result.HookCount > 0 {
		response.Metadata = mergeHookMetadata(response.Metadata, result)
	}
	if outcome == "completed" {
		p.emitFileChanged(ctx, call)
	}
	return response
}

// fileMutatingTools are the tools whose successful call changed a file the
// FileChanged hook should hear about.
var fileMutatingTools = map[string]bool{
	tools.EditToolName:          true,
	tools.MultiEditToolName:     true,
	tools.WriteToolName:         true,
	tools.RenameToolName:        true,
	tools.ReplaceSymbolToolName: true,
}

// emitFileChanged notifies observers that a file was written. Observational.
func (p *toolPipeline) emitFileChanged(ctx context.Context, call fantasy.ToolCall) {
	if p.runner == nil || !fileMutatingTools[call.Name] || !p.runner.HasEvent(hooks.EventFileChanged) {
		return
	}
	path := tools.RuleContentFor(call.Name, call.Input)
	if path == "" {
		return
	}
	input, err := json.Marshal(map[string]string{"tool": call.Name, "file_path": path})
	if err != nil {
		return
	}
	if _, err := p.runner.Run(ctx, hooks.EventFileChanged, tools.GetSessionFromContext(ctx), call.Name, string(input)); err != nil {
		slog.Warn("FileChanged hook failed", "tool", call.Name, "error", err)
	}
}

// emitPermissionDenied notifies observers that a call was refused. It never
// changes the outcome, so a hook cannot veto a veto.
func (p *toolPipeline) emitPermissionDenied(ctx context.Context, req permission.CreatePermissionRequest, reason string) {
	if p.runner == nil || !p.runner.HasEvent(hooks.EventPermissionDenied) {
		return
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return
	}
	input := string(payload)
	if reason != "" {
		if updated, err := sjson.Set(input, "reason", reason); err == nil {
			input = updated
		}
	}
	if _, err := p.runner.Run(ctx, hooks.EventPermissionDenied, req.SessionID, req.ToolName, input); err != nil {
		slog.Warn("PermissionDenied hook failed", "tool", req.ToolName, "error", err)
	}
}

func (p *toolPipeline) recordResult(ctx context.Context, call fantasy.ToolCall, response fantasy.ToolResponse, outcome string) {
	if p.recorder == nil {
		return
	}
	p.recorder.RecordToolResult(ctx, call, response, outcome)
}

// hookResultKey carries the aggregate hook result from the resolve stage to
// finalization without re-running hooks.
type hookResultKey struct{}
