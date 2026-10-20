package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ChatFunc is a minimal LLM chat: it takes a system prompt and a user
// prompt and returns the model's text response.
type ChatFunc func(ctx context.Context, systemPrompt, userPrompt string) (string, error)

// Editor is the LLM role that reads execution traces and edits
// harnesses. There is no ground truth: all three roles reason about
// agent behavior instead.
type Editor interface {
	// Propose inspects the base run and proposes up to k improved
	// harness variants.
	Propose(ctx context.Context, base *HarnessState, baseTrace *ExecutionTrace, k int) ([]*HarnessState, error)
	// Compose compares the base run with the candidate runs and
	// returns the best probe plus a merged harness.
	Compose(ctx context.Context, base *ProbeOutcome, candidates []ProbeOutcome) (*ComposeResult, error)
	// Reflect distills the improvement into a reusable lesson.
	Reflect(ctx context.Context, before, after *HarnessState, base, best *ExecutionTrace) (string, error)
}

// ComposeResult is the parsed output of the compose step.
type ComposeResult struct {
	// BestProbeID names the winning probe ("base" or one of the
	// candidate probe IDs).
	BestProbeID string `json:"best_probe_id"`
	// Rationale is the editor's explanation, kept for the trace.
	Rationale string `json:"rationale"`
	// Harness is the merged harness to persist for future tasks.
	Harness *HarnessState `json:"harness"`
}

// ChatEditor implements Editor on top of a ChatFunc.
type ChatEditor struct {
	chat ChatFunc
}

// NewChatEditor wraps a ChatFunc into an Editor.
func NewChatEditor(chat ChatFunc) *ChatEditor {
	return &ChatEditor{chat: chat}
}

const proposeSystemPrompt = `You are an agent-optimization architect. You improve the *harness* that
wraps a coding agent: the guardrails, tool guidance, memories, skills,
and helper scripts the agent sees in its system prompt.

Do not solve the user's task yourself. Diagnose why the agent's
process was suboptimal (e.g. it read irrelevant files, ran the whole
test suite instead of a focused subset, misused grep, lacked a
verification script, or misunderstood the repository layout), then
propose harness edits that would make a rerun behave better.

Return ONLY a JSON object of this exact shape:
{"candidates": [ { "id": "cand-1", "system_prompt": "", "guardrails":
["..."], "tool_guidance": {"bash": "...", "edit": "..."}, "memory":
["..."], "skills": {"...": "..."}, "scripts": {"check.sh": "..."} },
... ]}

Rules:
- Exactly the requested number of candidates.
- Each candidate must differ from the others in a meaningful way.
- Prefer small, targeted edits over rewriting everything.
- Guardrails are MUST/MUST NOT rules. Tool guidance is per-tool usage
  advice. Memory entries are short lessons. Scripts are complete,
  runnable bash scripts (shebang included).
- Do not include markdown fences or commentary outside the JSON.`

const composeSystemPrompt = `You are a behavioral-contrast judge for coding agent runs. You are shown
the base run and several candidate runs of the same task, each using a
different harness configuration. There is no ground truth for this
task: judge quality by comparing behavior.

Prefer, in order:
1. Runs that completed without errors.
2. Runs whose tool-call history shows verification (tests run, syntax
   checked) rather than blind edits.
3. Runs with few wasted steps (repeated reads of the same file, broad
   searches when a narrow one would do).
4. Runs whose git diff is focused and coherent for the task.

Return ONLY a JSON object of this exact shape:
{"best_probe_id": "<probe id>", "rationale": "<one or two sentences>",
"harness": { "id": "merged", "system_prompt": "", "guardrails": [...],
"tool_guidance": {...}, "memory": [...], "skills": {...}, "scripts":
{...} }}

Rules:
- best_probe_id must be one of the probe ids listed in the input.
- The harness must combine the strengths of the best run and any
  useful elements from other runs, and must keep prior lessons unless
  a run proved one wrong.
- Do not include markdown fences or commentary outside the JSON.`

const reflectSystemPrompt = `You distill agent-harness improvements into durable lessons. Compare
the harness before and after refinement, along with the base and
winning run traces, and identify the change that genuinely improved
the agent's behavior. Return ONE short lesson (at most three
sentences) written as a reusable rule for future tasks in this
repository. Do not add markdown or commentary.`

// Propose implements Editor.
func (e *ChatEditor) Propose(ctx context.Context, base *HarnessState, baseTrace *ExecutionTrace, k int) ([]*HarnessState, error) {
	if k < 1 {
		k = 1
	}
	var user strings.Builder
	fmt.Fprintf(&user, "Task:\n%s\n\n", baseTrace.TaskPrompt)
	fmt.Fprintf(&user, "Current harness:\n%s\n\n", FormatHarnessForLLM(base))
	fmt.Fprintf(&user, "Base run trace:\n%s\n\n", FormatTraceForLLM(baseTrace))
	fmt.Fprintf(&user, "Propose exactly %d candidate harnesses.\n", k)

	text, err := e.chat(ctx, proposeSystemPrompt, user.String())
	if err != nil {
		return nil, err
	}
	text = stripFences(text)

	var payload struct {
		Candidates []HarnessState `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return nil, fmt.Errorf("propose: could not parse LLM response: %w", err)
	}
	candidates := make([]*HarnessState, 0, k)
	for i := range payload.Candidates {
		c := payload.Candidates[i].Sanitize()
		if c.ID == "" {
			c.ID = fmt.Sprintf("cand-%d", i+1)
		}
		candidates = append(candidates, c)
		if len(candidates) >= k {
			break
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("propose: LLM proposed no candidates")
	}
	return candidates, nil
}

// Compose implements Editor.
func (e *ChatEditor) Compose(ctx context.Context, base *ProbeOutcome, candidates []ProbeOutcome) (*ComposeResult, error) {
	var user strings.Builder
	fmt.Fprintf(&user, "Task:\n%s\n\n", base.Trace.TaskPrompt)
	fmt.Fprintf(&user, "Base run:\n%s\n\n", FormatTraceForLLM(base.Trace))
	for i, c := range candidates {
		fmt.Fprintf(&user, "Candidate %d (harness %q):\n%s\n\n", i+1, c.Trace.HarnessID, FormatTraceForLLM(c.Trace))
	}
	fmt.Fprintf(&user, "Candidate harnesses:\n")
	for _, c := range candidates {
		fmt.Fprintf(&user, "- %q (probe %s):\n%s\n", c.Trace.HarnessID, c.Trace.ProbeID, FormatHarnessForLLM(c.Harness))
	}
	fmt.Fprintf(&user, "Base harness:\n%s\n", FormatHarnessForLLM(base.Harness))

	text, err := e.chat(ctx, composeSystemPrompt, user.String())
	if err != nil {
		return nil, err
	}
	text = stripFences(text)

	var res ComposeResult
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		return nil, fmt.Errorf("compose: could not parse LLM response: %w", err)
	}
	if res.BestProbeID == "" {
		return nil, fmt.Errorf("compose: LLM response has no best_probe_id")
	}
	if res.Harness == nil {
		return nil, fmt.Errorf("compose: LLM response has no merged harness")
	}
	res.Harness = res.Harness.Sanitize()
	if res.Harness.ID == "" {
		res.Harness.ID = "merged"
	}
	return &res, nil
}

// Reflect implements Editor.
func (e *ChatEditor) Reflect(ctx context.Context, before, after *HarnessState, base, best *ExecutionTrace) (string, error) {
	var user strings.Builder
	fmt.Fprintf(&user, "Harness before:\n%s\n\n", FormatHarnessForLLM(before))
	fmt.Fprintf(&user, "Harness after:\n%s\n\n", FormatHarnessForLLM(after))
	fmt.Fprintf(&user, "Base run:\n%s\n\n", FormatTraceForLLM(base))
	fmt.Fprintf(&user, "Winning run:\n%s\n", FormatTraceForLLM(best))

	lesson, err := e.chat(ctx, reflectSystemPrompt, user.String())
	if err != nil {
		return "", err
	}
	lesson = strings.TrimSpace(stripFences(lesson))
	if len(lesson) > 1_000 {
		lesson = lesson[:1_000]
	}
	return lesson, nil
}

// FormatHarnessForLLM renders a harness compactly for editor prompts.
func FormatHarnessForLLM(h *HarnessState) string {
	if h == nil {
		return "(empty harness)"
	}
	data, err := json.MarshalIndent(h.Sanitize(), "", "  ")
	if err != nil {
		return "(unrenderable harness)"
	}
	return string(data)
}

// stripFences removes markdown code fences LLMs like to wrap JSON in.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.IndexByte(s, '\n'); idx >= 0 {
			s = s[idx+1:]
		}
	}
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}
