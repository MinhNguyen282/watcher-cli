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
