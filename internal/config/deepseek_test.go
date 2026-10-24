package config

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/stretchr/testify/require"
)

func TestNormalizeDeepSeekCatalog(t *testing.T) {
	t.Parallel()
	source := []catwalk.Provider{{
		ID: catwalk.InferenceProviderDeepSeek, DefaultLargeModelID: "deepseek-v4-pro", DefaultSmallModelID: "deepseek-v4-flash",
		Models: []catwalk.Model{{ID: "deepseek-v4-flash", Name: "DeepSeek V4 Flash"}, {ID: "deepseek-v4-flash-vision-exp"}, {ID: "deepseek-v4-pro"}},
	}, {ID: "hyper", Models: []catwalk.Model{{ID: "deepseek-v4-flash-0731"}}}}
	got := normalizeDeepSeekCatalog(source)
	require.Equal(t, "deepseek-flash", got[0].DefaultSmallModelID)
	require.Equal(t, "deepseek-v4-pro", got[0].DefaultLargeModelID)
	require.Len(t, got[0].Models, 2)
	require.Equal(t, "deepseek-flash", got[0].Models[0].ID)
	require.Equal(t, "DeepSeek V4.1 Flash", got[0].Models[0].Name)
	require.Equal(t, int64(1_000_000), got[0].Models[0].ContextWindow)
	require.True(t, got[0].Models[0].SupportsImages)
	require.True(t, got[0].Models[0].CanReason)
	require.Equal(t, []string{"low", "high", "max", "none"}, got[0].Models[0].ReasoningLevels)
	require.Equal(t, "high", got[0].Models[0].DefaultReasoningEffort)
	require.Equal(t, []string{"low", "high", "max", "none"}, got[0].Models[1].ReasoningLevels)
	require.Equal(t, "high", got[0].Models[1].DefaultReasoningEffort)
	require.Equal(t, source[1], got[1])
	require.Equal(t, "deepseek-v4-flash", source[0].Models[0].ID, "Must not mutate cached/shared input")
	require.Equal(t, got, normalizeDeepSeekCatalog(got), "Normalization must be idempotent")
}

func TestNormalizeDeepSeekCatalogPrefersCurrentMetadata(t *testing.T) {
	t.Parallel()
	current := catwalk.Model{ID: "deepseek-flash", Name: "Future Flash", ContextWindow: 2_000_000, CostPer1MIn: 0.1}
	got := normalizeDeepSeekCatalog([]catwalk.Provider{{ID: catwalk.InferenceProviderDeepSeek,
		Models: []catwalk.Model{{ID: "deepseek-v4-flash"}, current}, DefaultLargeModelID: "deepseek-v4-flash"}})
	expected := current
	expected.CanReason = true
	expected.ReasoningLevels = []string{"low", "high", "max", "none"}
	expected.DefaultReasoningEffort = "high"
	require.Equal(t, []catwalk.Model{expected}, got[0].Models)
	require.Equal(t, "deepseek-flash", got[0].DefaultLargeModelID)
}

func TestNormalizeDeepSeekCatalogPreservesExplicitEfforts(t *testing.T) {
	t.Parallel()
	current := catwalk.Model{
		ID:                     "deepseek-flash",
		CanReason:              true,
		ReasoningLevels:        []string{"low", "max"},
		DefaultReasoningEffort: "max",
	}
	got := normalizeDeepSeekCatalog([]catwalk.Provider{{ID: catwalk.InferenceProviderDeepSeek,
		Models: []catwalk.Model{current}}})
	require.Equal(t, []string{"low", "max"}, got[0].Models[0].ReasoningLevels)
	require.Equal(t, "max", got[0].Models[0].DefaultReasoningEffort)
}

func TestNormalizeDeepSeekCatalogLeavesNonReasoningModels(t *testing.T) {
	t.Parallel()
	got := normalizeDeepSeekCatalog([]catwalk.Provider{{ID: catwalk.InferenceProviderDeepSeek,
		Models: []catwalk.Model{{ID: "deepseek-chat"}}}})
	model := got[0].Models[0]
	require.False(t, model.CanReason)
	require.Empty(t, model.ReasoningLevels)
	require.Empty(t, model.DefaultReasoningEffort)
}

func TestResolveDeepSeekLegacySlotsStayOnFlash(t *testing.T) {
	t.Parallel()
	providers := normalizeDeepSeekCatalog([]catwalk.Provider{{
		ID: catwalk.InferenceProviderDeepSeek, DefaultLargeModelID: "deepseek-v4-pro", DefaultSmallModelID: "deepseek-v4-flash",
		Models: []catwalk.Model{{ID: "deepseek-v4-flash", DefaultMaxTokens: 8192}, {ID: "deepseek-v4-pro", DefaultMaxTokens: 8192}},
	}})
	cfg := &Config{}
	cfg.setDefaults(t.TempDir(), "")
	cfg.Providers.Set("deepseek", ProviderConfig{ID: "deepseek", Models: providers[0].Models})
	for _, slot := range []SelectedModelType{SelectedModelTypeLarge, SelectedModelTypeSmall, SelectedModelTypeFast} {
		cfg.Models[slot] = SelectedModel{Provider: "deepseek", Model: "deepseek-v4-flash", MaxTokens: 8192, Think: true}
	}
	got, err := resolveSelectedModels(cfg, providers)
	require.NoError(t, err)
	for _, selected := range []SelectedModel{got.Large, got.Small, got.Fast} {
		require.Equal(t, "deepseek", selected.Provider)
		require.Equal(t, "deepseek-flash", selected.Model)
		require.Equal(t, int64(8192), selected.MaxTokens)
		require.True(t, selected.Think)
	}
	require.True(t, got.LargeFallback, "Canonical alias must be persisted")
	require.True(t, got.SmallFallback)
	require.True(t, got.FastFallback)
	require.Equal(t, "deepseek-v4-flash", cfg.Models[SelectedModelTypeLarge].Model, "Resolution must not mutate input")
}
