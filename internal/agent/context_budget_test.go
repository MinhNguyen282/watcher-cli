package agent

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

func TestContextBudgetIgnoresCompletionTokens(t *testing.T) {
	t.Parallel()

	budget := defaultContextBudget(100_000)
	// 70k context tokens plus 30k completion tokens is 100k total, but only
	// the context occupies the window. The old calculation compacted here.
	require.False(t, budget.shouldCompact(70_000))
	require.Equal(t, int64(30_000), budget.remaining(70_000))

	// Once the context itself crosses the same threshold, compaction runs.
	require.True(t, budget.shouldCompact(80_001))
}

func TestContextBudgetDefaults(t *testing.T) {
	t.Parallel()

	small := defaultContextBudget(100_000)
	require.True(t, small.shouldCompact(80_000))
	require.False(t, small.shouldCompact(79_999))

	large := defaultContextBudget(1_000_000)
	require.True(t, large.shouldCompact(980_000))
	require.False(t, large.shouldCompact(979_999))
}

func TestContextBudgetCustomThresholds(t *testing.T) {
	t.Parallel()

	opts := &config.Options{ContextCompaction: &config.ContextCompactionOptions{ThresholdRatio: 0.1}}
	budget := newContextBudget(100_000, opts)
	require.True(t, budget.shouldCompact(90_000))
	require.False(t, budget.shouldCompact(89_999))

	opts = &config.Options{ContextCompaction: &config.ContextCompactionOptions{HeadroomTokens: 5_000}}
	budget = newContextBudget(100_000, opts)
	require.True(t, budget.shouldCompact(95_000))
	require.False(t, budget.shouldCompact(94_999))
}

func TestContextBudgetClampsInvalidConfig(t *testing.T) {
	t.Parallel()

	opts := &config.Options{ContextCompaction: &config.ContextCompactionOptions{
		ThresholdRatio: 99,
		HeadroomTokens: 1_000_000,
	}}
	budget := newContextBudget(100_000, opts)
	require.Equal(t, int64(50_000), budget.headroom)
	require.True(t, budget.shouldCompact(50_000))
}
