package permission

import (
	"context"
	"fmt"
	"strings"
)

// Mode is the session-wide permission posture. It is deliberately a closed
// set: every surface (config, CLI flag, tool gate, UI) parses through
// ParseMode so an unknown value is a startup error instead of a silent
// downgrade to permissive behavior.
type Mode string

const (
	// ModeDefault asks the user for every call that is not covered by a
	// rule, a session grant, or the read-only allowlist.
	ModeDefault Mode = "default"
	// ModePlan is read-only: mutating tools are denied by the tool pipeline
	// regardless of what the model asks for.
	ModePlan Mode = "plan"
	// ModeAcceptEdits auto-approves file edits but still asks for shell and
	// network calls.
	ModeAcceptEdits Mode = "acceptEdits"
	// ModeAuto auto-approves read-only work and defers everything else to
	// the user. It is the mode a "careful automation" profile wants.
	ModeAuto Mode = "auto"
	// ModeBypassPermissions approves everything. It can be disabled by
	// configuration, in which case selecting it is an error.
	ModeBypassPermissions Mode = "bypassPermissions"
	// ModeDontAsk never prompts: anything not already allowed is denied.
	ModeDontAsk Mode = "dontAsk"
)

// Modes is the ordered list of known modes. The order doubles as the
// escalation rank: a later mode is strictly more permissive than an earlier
// one, except for dontAsk which trades permission for silence.
var Modes = []Mode{
	ModePlan,
	ModeDontAsk,
	ModeDefault,
	ModeAcceptEdits,
	ModeAuto,
	ModeBypassPermissions,
}

// Next returns the next selectable mode in the permissiveness ring.
// Bypass is skipped when the deployment disabled it, and an unknown current
// mode restarts the ring at ModeDefault.
func (m Mode) Next(bypassAllowed bool) Mode {
	index := -1
	for i, mode := range Modes {
		if mode == m {
			index = i
			break
		}
	}
	if index < 0 {
		return ModeDefault
	}
	for step := 1; step <= len(Modes); step++ {
		candidate := Modes[(index+step)%len(Modes)]
		if candidate == ModeBypassPermissions && !bypassAllowed {
			// Bypass sits between auto and plan in the ring; restarting
			// at the default is friendlier than jumping to plan.
			return ModeDefault
		}
		return candidate
	}
	return ModeDefault
}

// ParseMode normalizes a mode string. An empty string means ModeDefault.
func ParseMode(value string) (Mode, error) {
	switch trimmed := strings.TrimSpace(value); Mode(trimmed) {
	case "":
		return ModeDefault, nil
	case ModeDefault, ModePlan, ModeAcceptEdits, ModeAuto, ModeDontAsk, ModeBypassPermissions:
		return Mode(trimmed), nil
	default:
		names := make([]string, 0, len(Modes))
		for _, mode := range Modes {
			names = append(names, string(mode))
		}
		return "", fmt.Errorf("unknown permission mode %q: must be one of %s", value, strings.Join(names, ", "))
	}
}

// Rank returns the permissiveness rank of the mode: a higher rank can do
// everything a lower rank can do and more. ModePlan is the most restrictive,
// ModeDontAsk is next because it silently refuses anything that would prompt.
func (m Mode) Rank() int {
	switch m {
	case ModePlan:
		return 0
	case ModeDontAsk:
		return 1
	case ModeDefault:
		return 2
	case ModeAcceptEdits:
		return 3
	case ModeAuto:
		return 4
	case ModeBypassPermissions:
		return 5
	default:
		return 2
	}
}

// StrongerThan reports whether m is more permissive than other.
func (m Mode) StrongerThan(other Mode) bool {
	return m.Rank() > other.Rank()
}

// Label is the human-facing name shown in the UI.
func (m Mode) Label() string {
	switch m {
	case ModePlan:
		return "Plan"
	case ModeAcceptEdits:
		return "Accept edits"
	case ModeAuto:
		return "Auto"
	case ModeBypassPermissions:
		return "Bypass permissions"
	case ModeDontAsk:
		return "Don't ask"
	default:
		return "Default"
	}
}

// ValidateTransition checks a requested mode change. Downgrades are always
// allowed. Escalation into bypass is refused when the deployment disabled
// it, and leaving plan mode requires an explicit approval so the model
// cannot end its own read-only phase.
func ValidateTransition(from, to Mode, options ModeOptions) error {
	if _, err := ParseMode(string(to)); err != nil {
		return err
	}
	if from == to {
		return nil
	}
	if to == ModeBypassPermissions && !options.BypassAllowed {
		return fmt.Errorf("bypass permissions mode is disabled by configuration")
	}
	if from == ModePlan && !options.PlanExitApproved {
		return fmt.Errorf("leaving plan mode requires explicit approval")
	}
	return nil
}

// ModeOptions carries the deployment switches that affect transitions.
type ModeOptions struct {
	// BypassAllowed reports whether bypassPermissions may be selected.
	BypassAllowed bool
	// PlanExitApproved reports whether the user approved leaving plan mode
	// for this transition.
	PlanExitApproved bool
}

// ModeService is the optional capability a permission service exposes when
// it can actually enforce modes and rules. Callers type-assert to it so the
// plain Service contract stays small and test doubles stay cheap.
type ModeService interface {
	SetMode(mode Mode)
	Mode() Mode
	SetModeOptions(options ModeOptions)
	ModeOptions() ModeOptions
	SetRules(rules []Rule)
	Rules() []Rule
	Decide(ctx context.Context, opts CreatePermissionRequest) Decision
	DecisionFor(toolCallID string) (Decision, bool)
}

// CanAutoApprove reports whether the mode approves a call without asking,
// given that the call is otherwise allowed. It is the mode half of Decide.
func (m Mode) CanAutoApprove(specReadOnly bool) bool {
	switch m {
	case ModeBypassPermissions:
		return true
	case ModeAuto:
		return specReadOnly
	case ModeAcceptEdits:
		return specReadOnly
	case ModePlan:
		return specReadOnly
	default:
		return false
	}
}
