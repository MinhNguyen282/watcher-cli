package permission

// Stage names the stage that produced a permission decision. Recording the
// stage instead of a bare boolean is what makes a denial explainable after
// the fact: the UI, logs and tool metadata can all say who decided.
type Stage string

const (
	// StageSkip means prompts are disabled (yolo / skip-requests).
	StageSkip Stage = "skip"
	// StageAllowlist means the harness allowlist covered the call: a
	// configured allowed tool or a read-only shell command.
	StageAllowlist Stage = "allowlist"
	// StageRuleAllow means an allow rule matched.
	StageRuleAllow Stage = "rule-allow"
	// StageRuleAsk means an ask rule forced a prompt.
	StageRuleAsk Stage = "rule-ask"
	// StageRuleDeny means a deny rule matched.
	StageRuleDeny Stage = "rule-deny"
	// StageHook means a PreToolUse hook approved the call.
	StageHook Stage = "hook"
	// StageSession means the user already approved this tool, action and
	// path for the session.
	StageSession Stage = "session"
	// StageModePlan means plan mode refused a mutating call.
	StageModePlan Stage = "mode-plan"
	// StageModeDontAsk means dontAsk mode refused a call that would have
	// prompted.
	StageModeDontAsk Stage = "mode-dont-ask"
	// StageModeAuto means auto mode approved a read-only call.
	StageModeAuto Stage = "mode-auto"
	// StageModeBypass means bypassPermissions approved the call.
	StageModeBypass Stage = "mode-bypass"
	// StageUser means the user answered an interactive prompt. It is
	// recorded on resolution, not produced by Decide.
	StageUser Stage = "user"
	// StageUnavailable means no decision could be made. Callers fail closed.
	StageUnavailable Stage = "unavailable"
)

// Decision is the outcome of the non-interactive part of a permission check.
// Prompt is true when the caller must ask the user; Granted is only
// meaningful when Prompt is false.
type Decision struct {
	Granted bool   `json:"granted"`
	Prompt  bool   `json:"prompt"`
	Stage   Stage  `json:"stage"`
	Reason  string `json:"reason,omitempty"`
	Rule    Rule   `json:"rule,omitempty"`
}

// Allowed reports whether the call may run without a prompt.
func (d Decision) Allowed() bool { return !d.Prompt && d.Granted }

// Denied reports whether the call is refused without a prompt.
func (d Decision) Denied() bool { return !d.Prompt && !d.Granted }
