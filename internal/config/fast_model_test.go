package config

import (
	"context"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/env"
	"github.com/stretchr/testify/require"
)

// fastModelFixture returns a minimal configured config with two known
// provider models.
func fastModelFixture(t *testing.T) (*Config, []catwalk.Provider) {
	t.Helper()
	dir := t.TempDir()
	knownProviders := []catwalk.Provider{
		{
			ID:                  "openai",
			APIKey:              "abc",
			DefaultLargeModelID: "large-model",
			DefaultSmallModelID: "small-model",
			Models: []catwalk.Model{
				{ID: "large-model", DefaultMaxTokens: 1000},
				{ID: "small-model", DefaultMaxTokens: 500},
				{ID: "mini-model", DefaultMaxTokens: 200},
			},
		},
	}
	cfg := &Config{}
	cfg.setDefaults(dir, "")
	store := &ConfigStore{config: cfg}
	env := env.NewFromMap(map[string]string{})
	resolver := NewShellVariableResolver(env)
	require.NoError(t, cfg.configureProviders(context.Background(), store, env, resolver, knownProviders))
	return cfg, knownProviders
}

func TestResolveSelectedModelsFastDefaultsToSmall(t *testing.T) {
	t.Parallel()
	cfg, providers := fastModelFixture(t)

	resolved, err := resolveSelectedModels(cfg, providers)
	require.NoError(t, err)
	require.False(t, resolved.FastFallback,
		"an unset fast slot falls back silently without a correction")
	require.Equal(t, resolved.Small.Provider, resolved.Fast.Provider)
	require.Equal(t, resolved.Small.Model, resolved.Fast.Model)
}

func TestResolveSelectedModelsFastConfigured(t *testing.T) {
	t.Parallel()
	cfg, providers := fastModelFixture(t)
	cfg.Models[SelectedModelTypeFast] = SelectedModel{Provider: "openai", Model: "mini-model"}

	resolved, err := resolveSelectedModels(cfg, providers)
	require.NoError(t, err)
	require.False(t, resolved.FastFallback)
	require.Equal(t, "openai", resolved.Fast.Provider)
	require.Equal(t, "mini-model", resolved.Fast.Model)
	require.Equal(t, int64(200), resolved.Fast.MaxTokens)
}

func TestResolveSelectedModelsFastInvalidFallsBack(t *testing.T) {
	t.Parallel()
	cfg, providers := fastModelFixture(t)
	cfg.Models[SelectedModelTypeFast] = SelectedModel{Provider: "openai", Model: "ghost-model"}

	resolved, err := resolveSelectedModels(cfg, providers)
	require.NoError(t, err)
	require.True(t, resolved.FastFallback)
	require.Equal(t, resolved.Small.Provider, resolved.Fast.Provider)
	require.Equal(t, resolved.Small.Model, resolved.Fast.Model)
}

func TestSetupAgentsTaskUsesFastSlot(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.setDefaults(t.TempDir(), "")
	cfg.SetupAgents()

	task, ok := cfg.Agents[AgentTask]
	require.True(t, ok)
	require.Equal(t, SelectedModelTypeFast, task.Model,
		"the task sub-agent must run on the fast slot")

	coder, ok := cfg.Agents[AgentCoder]
	require.True(t, ok)
	require.Equal(t, SelectedModelTypeLarge, coder.Model)
}
