package agent

import "github.com/charmbracelet/crush/internal/config"

const (
	// largeContextWindowThreshold is where the default strategy switches
	// from a ratio of the window to a fixed headroom.
	largeContextWindowThreshold = 200_000
	// largeContextWindowBuffer is the default headroom for large windows.
	largeContextWindowBuffer = 20_000
	// smallContextWindowRatio is the default remaining-window ratio for
	// smaller windows.
	smallContextWindowRatio = 0.2
	// precomputeRatio is the share of the compaction trigger at which a
	// background summary is prepared.
	precomputeRatio = 0.75
	// idleCompactRatio is the share of the trigger at which an idle
	// session compacts itself.
	idleCompactRatio = 0.9
)

// contextBudget decides when a session needs compaction. It compares
// context tokens only: completion tokens are output, not context pressure,
// and adding them double-counts the same request.
type contextBudget struct {
	window   int64
	headroom int64
	ratio    float64
}

func defaultContextBudget(window int64) contextBudget {
	return newContextBudget(window, nil)
}

func newContextBudget(window int64, opts *config.Options) contextBudget {
	budget := contextBudget{window: window}
	if window <= 0 {
		return budget
	}

	var configured bool
	var configuredHeadroom int64
	var configuredRatio float64
	if opts != nil && opts.ContextCompaction != nil {
		configured = opts.ContextCompaction.HeadroomTokens > 0 || opts.ContextCompaction.ThresholdRatio > 0
	}
	configuredHeadroom, configuredRatio = opts.NormalizedContextCompaction(window)

	switch {
	case configured && configuredHeadroom > 0:
		budget.headroom = configuredHeadroom
	case configured && configuredRatio > 0:
		budget.ratio = configuredRatio
	case window > largeContextWindowThreshold:
		budget.headroom = largeContextWindowBuffer
	default:
		budget.ratio = smallContextWindowRatio
	}
	return budget
}

// shouldCompact reports whether the remaining context is inside the
// configured threshold.
func (b contextBudget) shouldCompact(contextTokens int64) bool {
	if b.window <= 0 {
		return false
	}
	if contextTokens < 0 {
		contextTokens = 0
	}
	remaining := b.window - contextTokens
	if b.headroom > 0 {
		return remaining <= b.headroom
	}
	return remaining <= int64(float64(b.window)*b.ratio)
}

// remaining returns how many context tokens are left before the window is
// exhausted.
func (b contextBudget) remaining(contextTokens int64) int64 {
	return b.window - contextTokens
}

// precomputeDue reports whether a background summary should be prepared.
// Precomputing starts at precomputeRatio of the compaction trigger so the
// summary is ready before the turn that needs it, without paying for it on
// every session.
func (b contextBudget) precomputeDue(contextTokens int64) bool {
	return b.dueAt(contextTokens, precomputeRatio)
}

// idleCompactDue reports whether an idle session is close enough to the
// trigger to compact before the user comes back.
func (b contextBudget) idleCompactDue(contextTokens int64) bool {
	return b.dueAt(contextTokens, idleCompactRatio)
}

func (b contextBudget) dueAt(contextTokens int64, ratio float64) bool {
	trigger := b.trigger()
	if trigger <= 0 {
		return false
	}
	if contextTokens < 0 {
		contextTokens = 0
	}
	return contextTokens >= int64(float64(trigger)*ratio)
}

// trigger is the context size at which shouldCompact turns true.
func (b contextBudget) trigger() int64 {
	if b.window <= 0 {
		return 0
	}
	if b.headroom > 0 {
		return b.window - b.headroom
	}
	return b.window - int64(float64(b.window)*b.ratio)
}
