// Package hooks runs user-defined shell commands that fire on hook events
// (e.g. PreToolUse), returning decisions that control agent behavior.
package hooks

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/tidwall/sjson"
)

// Hook event name constants. Events are grouped by what can act on their
// output: gating events can allow/deny, observational events can only add
// context.
const (
	// Gating events.

	// EventPreToolUse runs before a tool call and may allow, deny or
	// rewrite its input.
	EventPreToolUse = "PreToolUse"
	// EventPermissionRequest runs when a call would prompt the user.
	EventPermissionRequest = "PermissionRequest"
	// EventUserPromptSubmit runs before a prompt is sent to the model.
	EventUserPromptSubmit = "UserPromptSubmit"
	// EventPreCompact runs before compaction and may veto it.
	EventPreCompact = "PreCompact"

	// Observational events.

	// EventPostToolUse runs after a tool call completes successfully.
	EventPostToolUse = "PostToolUse"
	// EventPostToolUseFailure runs after a tool call fails.
	EventPostToolUseFailure = "PostToolUseFailure"
	// EventPostToolBatch runs once per step with every call the step made.
	EventPostToolBatch = "PostToolBatch"
	// EventPermissionDenied runs after a call is denied.
	EventPermissionDenied = "PermissionDenied"
	// EventPostCompact runs after compaction completes.
	EventPostCompact = "PostCompact"
	// EventStop runs when a turn ends.
	EventStop = "Stop"
	// EventSessionStart runs when a session starts its first turn.
	EventSessionStart = "SessionStart"
	// EventSessionEnd runs when a session is closed or deleted.
	EventSessionEnd = "SessionEnd"
	// EventSubagentStart runs when a sub-agent is launched.
	EventSubagentStart = "SubagentStart"
	// EventSubagentStop runs when a sub-agent finishes.
	EventSubagentStop = "SubagentStop"
	// EventFileChanged runs when a tracked file changes outside the agent.
	EventFileChanged = "FileChanged"
	// EventCwdChanged runs when the working directory changes.
	EventCwdChanged = "CwdChanged"
	// EventInstructionsLoaded runs when context files are loaded.
	EventInstructionsLoaded = "InstructionsLoaded"
	// EventTaskCreated and EventTaskCompleted bracket a task-list item.
	EventTaskCreated   = "TaskCreated"
	EventTaskCompleted = "TaskCompleted"
	// EventNotification is emitted when the user is notified.
	EventNotification = "Notification"
	// EventMessageDisplay observes a message before it is displayed.
	EventMessageDisplay = "MessageDisplay"
)

// Events is every event this build runs hooks for, in a stable order.
var Events = []string{
	EventPreToolUse,
	EventPostToolUse,
	EventPostToolUseFailure,
	EventPostToolBatch,
	EventPermissionRequest,
	EventPermissionDenied,
	EventUserPromptSubmit,
	EventSessionStart,
	EventSessionEnd,
	EventStop,
	EventSubagentStart,
	EventSubagentStop,
	EventPreCompact,
	EventPostCompact,
	EventFileChanged,
	EventCwdChanged,
	EventInstructionsLoaded,
	EventTaskCreated,
	EventTaskCompleted,
	EventNotification,
	EventMessageDisplay,
}

// IsEvent reports whether name is a known hook event.
func IsEvent(name string) bool {
	for _, event := range Events {
		if event == name {
			return true
		}
	}
	return false
}

// HaltExitCode is the exit code that halts the whole turn. 2 blocks the
// current tool call; 49 sits in the no-man's-land between the
// generic-error range (1-30), the sysexits range (64-78), and the
// killed-by-signal range (128+) so it can't be hit by accident.
const HaltExitCode = 49

// HookMetadata is embedded in tool response metadata so the UI can
// display a hook indicator.
type HookMetadata struct {
	HookCount    int        `json:"hook_count"`
	Decision     string     `json:"decision"`
	Halt         bool       `json:"halt,omitempty"`
	Reason       string     `json:"reason,omitempty"`
	InputRewrite bool       `json:"input_rewrite,omitempty"`
	Hooks        []HookInfo `json:"hooks,omitempty"`
}

// HookInfo identifies a single hook that ran and its individual result.
type HookInfo struct {
	Name         string `json:"name"`
	Matcher      string `json:"matcher,omitempty"`
	Decision     string `json:"decision"`
	Halt         bool   `json:"halt,omitempty"`
	Reason       string `json:"reason,omitempty"`
	InputRewrite bool   `json:"input_rewrite,omitempty"`
}

// Decision represents the outcome of a single hook execution.
type Decision int

const (
	// DecisionNone means the hook expressed no opinion.
	DecisionNone Decision = iota
	// DecisionAllow means the hook explicitly allowed the action.
	DecisionAllow
	// DecisionDeny means the hook blocked the action.
	DecisionDeny
)

func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionDeny:
		return "deny"
	default:
		return "none"
	}
}

// HookResult holds the parsed output of a single hook execution.
type HookResult struct {
	Decision     Decision
	Halt         bool   // If true, halt the whole turn.
	Reason       string // Deny or halt reason (same field, different audience).
	Context      string
	UpdatedInput string // Shallow-merge patch against tool_input (opaque JSON).
}

// AggregateResult holds the combined outcome of all hooks for an event.
type AggregateResult struct {
	Decision     Decision
	Halt         bool       // Any hook requested halt.
	HookCount    int        // Number of hooks that ran.
	Hooks        []HookInfo // Info about each hook that ran (config order).
	Reason       string     // Concatenated deny/halt reasons (newline-separated).
	Context      string     // Concatenated context from all hooks.
	UpdatedInput string     // Merged tool_input JSON (empty if no patches).
}

// aggregate merges multiple HookResults into a single AggregateResult.
// Results are processed in config order (the order of the slice). Deny
// wins over allow, allow wins over none. Halt is sticky. Reasons and
// context concatenate in order. updated_input patches shallow-merge in
// order against the original tool input; later patches override earlier
// ones on colliding keys.
func aggregate(results []HookResult, origToolInput string) AggregateResult {
	var (
		decision Decision
		halt     bool
		reasons  []string
		contexts []string
		merged   = origToolInput
		anyPatch = false
	)
	for _, r := range results {
		switch r.Decision {
		case DecisionDeny:
			decision = DecisionDeny
			if r.Reason != "" {
				reasons = append(reasons, r.Reason)
			}
		case DecisionAllow:
			if decision != DecisionDeny {
				decision = DecisionAllow
			}
		case DecisionNone:
			// No change.
		}
		if r.Halt {
			halt = true
			if r.Reason != "" && r.Decision != DecisionDeny {
				// A halting hook that didn't also deny still contributes
				// its reason so the user sees it.
				reasons = append(reasons, r.Reason)
			}
		}
		if r.Context != "" {
			contexts = append(contexts, r.Context)
		}
		if r.UpdatedInput != "" {
			next, err := shallowMerge(merged, r.UpdatedInput)
			if err != nil {
				slog.Warn(
					"Hook updated_input patch rejected; ignoring",
					"error", err,
					"patch", r.UpdatedInput,
				)
				continue
			}
			merged = next
			anyPatch = true
		}
	}

	agg := AggregateResult{
		Decision:  decision,
		Halt:      halt,
		HookCount: len(results),
	}
	if anyPatch {
		agg.UpdatedInput = merged
	}
	if len(reasons) > 0 {
		agg.Reason = strings.Join(reasons, "\n")
	}
	if len(contexts) > 0 {
		agg.Context = strings.Join(contexts, "\n")
	}
	return agg
}

// shallowMerge applies a top-level-keys patch to base (both JSON
// objects). Keys in patch overwrite keys in base; keys absent from the
// patch are preserved. Returns an error if either value is not a valid
// JSON object.
func shallowMerge(base, patch string) (string, error) {
	if base == "" {
		base = "{}"
	}
	// Ensure base is an object so sjson has somewhere to write.
	var baseAny any
	if err := json.Unmarshal([]byte(base), &baseAny); err != nil {
		return "", err
	}
	if _, ok := baseAny.(map[string]any); !ok {
		return "", errNotObject("tool_input")
	}
	var patchMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(patch), &patchMap); err != nil {
		return "", errNotObject("updated_input")
	}
	out := base
	for k, v := range patchMap {
		next, err := sjson.SetRawBytes([]byte(out), k, v)
		if err != nil {
			return "", err
		}
		out = string(next)
	}
	return out, nil
}

type errNotObject string

func (e errNotObject) Error() string { return string(e) + " is not a JSON object" }
