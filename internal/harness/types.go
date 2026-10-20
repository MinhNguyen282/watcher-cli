// Package harness implements the self-evolving agent harness ("Harness²").
//
// A harness is the collection of editable artifacts that surround the
// LLM — system prompt additions, guardrails, tool guidance, memories,
// skills, and helper scripts. Instead of asking the user to maintain
// these by hand, the harness optimizer runs the Propose → Probe →
// Compose loop: it observes how the agent behaved on a task, asks an
// editor LLM to propose improved harness variants, probes those
// variants in isolated git worktrees, compares the resulting behaviors
// (behavioral contrast, no ground truth required), and persists the
// winning harness plus an evidence-backed lesson for future tasks.
package harness

import (
	"fmt"
	"strings"
	"time"
)

// HarnessState is the editable configuration surrounding an agent run.
// Every field is optional: only non-empty sections are rendered into
// the agent's system prompt.
type HarnessState struct {
	ID string `json:"id"`
	// SystemPrompt is appended verbatim to the agent's system prompt.
	// It is the escape hatch for guidance that does not fit the
	// structured fields below.
	SystemPrompt string `json:"system_prompt"`
	// Guardrails are hard rules the agent must obey (for example
	// "Never edit files under pkg/vendor/").
	Guardrails []string `json:"guardrails"`
	// ToolGuidance maps a tool name (bash, edit, grep, ...) to
	// instructions on how to use it effectively in this repository.
	ToolGuidance map[string]string `json:"tool_guidance"`
	// Memory holds lessons learned from previous runs on this
	// repository.
	Memory []string `json:"memory"`
	// Skills maps a skill name to its procedure (SOP) content.
	Skills map[string]string `json:"skills"`
	// Scripts maps a script file name to its content. Scripts are
	// written to .watcher/scripts/ in the probe workspace so the agent
	// can execute them through the bash tool.
	Scripts map[string]string `json:"scripts"`
	// RenderedSystemPrompt is the final prompt section computed from
	// the fields above. It is refreshed whenever the harness is saved
	// and read verbatim by the agent at startup.
	RenderedSystemPrompt string `json:"rendered_system_prompt"`
}

// Limits that keep harness states bounded even when produced by a
// misbehaving editor LLM.
const (
	maxGuardrails   = 20
	maxToolGuidance = 20
	maxMemory       = 50
	maxSkills       = 20
	maxScripts      = 20
	maxFieldLen     = 8_000
	maxTotalRender  = 40_000
)

// Sanitize trims every field, drops empty entries, and clamps sizes to
// the package limits. It returns a new, normalized harness.
func (h *HarnessState) Sanitize() *HarnessState {
	trim := func(s string) string {
		if len(s) > maxFieldLen {
			return strings.TrimSpace(s[:maxFieldLen])
		}
		return strings.TrimSpace(s)
	}
	out := &HarnessState{
		ID:           trim(h.ID),
		SystemPrompt: trim(h.SystemPrompt),
	}
	for _, g := range h.Guardrails {
		if g = trim(g); g != "" && len(out.Guardrails) < maxGuardrails {
			out.Guardrails = append(out.Guardrails, g)
		}
	}
	out.ToolGuidance = make(map[string]string)
	for k, v := range h.ToolGuidance {
		if k = trim(k); k == "" || len(out.ToolGuidance) >= maxToolGuidance {
			continue
		}
		out.ToolGuidance[k] = trim(v)
	}
	for _, m := range h.Memory {
		if m = trim(m); m != "" && len(out.Memory) < maxMemory {
			out.Memory = append(out.Memory, m)
		}
	}
	out.Skills = make(map[string]string)
	for k, v := range h.Skills {
		if k = trim(k); k == "" || len(out.Skills) >= maxSkills {
			continue
		}
		out.Skills[k] = trim(v)
	}
	out.Scripts = make(map[string]string)
	for k, v := range h.Scripts {
		if k = trim(k); k == "" || len(out.Scripts) >= maxScripts {
			continue
		}
		out.Scripts[k] = trim(v)
	}
	return out
}

// RenderSection renders the harness into the prompt section that gets
// appended to the agent's system prompt. Empty harnesses render to an
// empty string.
func (h *HarnessState) RenderSection() string {
	if h == nil {
		return ""
	}
	var sb strings.Builder
	write := func(s string) {
		sb.WriteString(s)
	}
	write("<self-evolving-harness>\n")
	write("The rules below were produced by the harness optimizer for this repository. Treat them as project instructions with the same priority as memory files.\n")

	if h.SystemPrompt != "" {
		write("\n<harness_system_prompt>\n")
		write(h.SystemPrompt)
		write("\n</harness_system_prompt>\n")
	}
	if len(h.Guardrails) > 0 {
		write("\n<guardrails>\n")
		for _, g := range h.Guardrails {
			write("- " + g + "\n")
		}
		write("</guardrails>\n")
	}
	if len(h.ToolGuidance) > 0 {
		write("\n<tool_guidance>\n")
		for tool, guidance := range h.ToolGuidance {
			write("### " + tool + "\n" + guidance + "\n")
		}
		write("</tool_guidance>\n")
	}
	if len(h.Memory) > 0 {
		write("\n<lessons_learned>\n")
		for _, m := range h.Memory {
			write("- " + m + "\n")
		}
		write("</lessons_learned>\n")
	}
	if len(h.Skills) > 0 {
		write("\n<harness_skills>\n")
		for name, content := range h.Skills {
			write("### Skill: " + name + "\n" + content + "\n")
		}
		write("</harness_skills>\n")
	}
	if len(h.Scripts) > 0 {
		write("\n<helper_scripts>\n")
		write("The following scripts are written to .watcher/scripts/ in this workspace. Run them with the bash tool when relevant.\n")
		for name := range h.Scripts {
			write("- .watcher/scripts/" + name + "\n")
		}
		write("</helper_scripts>\n")
	}
	write("</self-evolving-harness>")

	rendered := sb.String()
	if len(rendered) > maxTotalRender {
		rendered = rendered[:maxTotalRender]
	}
	return rendered
}

// RefreshRendered recomputes RenderedSystemPrompt from the structured
// fields. Store.SaveHarness calls it automatically.
func (h *HarnessState) RefreshRendered() {
	if h == nil {
		return
	}
	h.RenderedSystemPrompt = h.RenderSection()
}

// ToolCallRecord is one tool invocation observed during an agent run.
type ToolCallRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Input   string `json:"input"`
	Output  string `json:"output"`
	IsError bool   `json:"is_error"`
}

// ExecutionTrace is the behavioral evidence collected after one agent
// run on a task. The optimizer compares traces (not outputs) because
// real-world tasks have no ground truth.
type ExecutionTrace struct {
	ProbeID    string `json:"probe_id"`
	HarnessID  string `json:"harness_id"`
	TaskPrompt string `json:"task_prompt"`
	// WorkingDir and Branch identify the isolated worktree the probe
	// ran in.
	WorkingDir string `json:"working_dir"`
	Branch     string `json:"branch"`
	// ToolCalls is the full tool call history recovered from the
	// probe's message store.
	ToolCalls []ToolCallRecord `json:"tool_calls"`
	// GitDiff is the code change produced by the run, captured with
	// `git diff` in the probe worktree.
	GitDiff string `json:"git_diff"`
	// FinalOutput is the agent's final text response.
	FinalOutput string `json:"final_output"`
	// ProbeOutput is the raw stderr/stdout noise of the probe process,
	// kept for debugging and for the compose step.
	ProbeOutput string `json:"probe_output,omitempty"`
	// Error is non-empty when the probe failed (timeout, crash, tool
	// loop). Failed probes still participate in behavioral contrast.
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
	Duration  time.Duration `json:"-"`
	DurationMs int64     `json:"duration_ms"`
}

// Stats summarizes tool usage for the behavioral-contrast prompt.
type Stats struct {
	ToolCalls int            `json:"tool_calls"`
	ToolErrors int           `json:"tool_errors"`
	ByTool    map[string]int `json:"by_tool"`
}

// ComputeStats derives tool usage statistics from the trace.
func (t *ExecutionTrace) ComputeStats() Stats {
	s := Stats{ByTool: make(map[string]int)}
	for _, tc := range t.ToolCalls {
		s.ToolCalls++
		s.ByTool[tc.Name]++
		if tc.IsError {
			s.ToolErrors++
		}
	}
	return s
}

// ProbeSpec describes a single probe run handed to a ProbeRunner.
type ProbeSpec struct {
	ID      string
	Harness *HarnessState
	WorkDir string
	Branch  string
	Task    string
}

// ProbeOutcome pairs a probe's trace with the harness that produced it.
type ProbeOutcome struct {
	Trace   *ExecutionTrace
	Harness *HarnessState
}

// Mode selects between parallel and sequential probing.
type Mode string

const (
	// ModeParallel probes every candidate from the same base ref in
	// isolated worktrees at the same time.
	ModeParallel Mode = "parallel"
	// ModeSequential probes candidates one at a time, chaining each
	// candidate's worktree on the previous candidate's branch.
	ModeSequential Mode = "sequential"
)

// ValidMode reports whether m is a supported probe mode.
func ValidMode(m Mode) bool {
	return m == ModeParallel || m == ModeSequential
}

// RunOptions configures one Optimizer.Run cycle.
type RunOptions struct {
	// Probes is K, the number of candidate harnesses to generate.
	Probes int
	// Mode selects parallel or sequential probing.
	Mode Mode
	// Timeout bounds each probe run. Zero means no per-probe bound.
	Timeout time.Duration
	// ApplyWinner applies the winning diff to the main workspace.
	ApplyWinner bool
	// Log, when non-nil, receives progress lines.
	Log func(format string, args ...any)
}

// DefaultRunOptions returns sensible defaults for a run.
func DefaultRunOptions() RunOptions {
	return RunOptions{
		Probes:      2,
		Mode:        ModeParallel,
		Timeout:     20 * time.Minute,
		ApplyWinner: true,
	}
}

// RunResult is what Optimizer.Run returns when it succeeds.
type RunResult struct {
	Base        *ExecutionTrace
	Candidates  []ProbeOutcome
	Best        *ProbeOutcome
	Harness     *HarnessState
	Lesson      string
	Applied     bool
	KeptBranch  string
}

// Summary renders a short human-readable report of the run.
func (r *RunResult) Summary() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Base probe:        %s\n", r.Base.ProbeID)
	if r.Base.Error != "" {
		fmt.Fprintf(&sb, "  error:           %s\n", r.Base.Error)
	}
	fmt.Fprintf(&sb, "Candidates probed: %d\n", len(r.Candidates))
	if r.Best != nil {
		fmt.Fprintf(&sb, "Winner:            %s (harness %q)\n", r.Best.Trace.ProbeID, r.Best.Trace.HarnessID)
	}
	if r.Lesson != "" {
		fmt.Fprintf(&sb, "Lesson saved:      .watcher/memory/lessons.md\n")
	}
	switch {
	case r.KeptBranch != "":
		fmt.Fprintf(&sb, "Winner diff kept on branch %q (apply with: git merge %s)\n", r.KeptBranch, r.KeptBranch)
	case r.Applied:
		fmt.Fprint(&sb, "Winner diff applied to the working tree.\n")
	default:
		fmt.Fprint(&sb, "Winner diff was not applied.\n")
	}
	return sb.String()
}
