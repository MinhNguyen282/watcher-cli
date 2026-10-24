package config

import (
	"slices"
	"strings"

	"charm.land/catwalk/pkg/catwalk"
)

// normalizeDeepSeekCatalog upgrades retired Flash aliases in cached, embedded
// and fetched catalogs. Prefer metadata for the current alias when available.
// Other providers, including Hyper's independently versioned IDs, are untouched.
func normalizeDeepSeekCatalog(providers []catwalk.Provider) []catwalk.Provider {
	result := slices.Clone(providers)
	for i, provider := range result {
		if provider.ID != catwalk.InferenceProviderDeepSeek {
			continue
		}
		var current *catwalk.Model
		for _, model := range provider.Models {
			if model.ID == "deepseek-flash" {
				current = &model
				break
			}
		}
		models := make([]catwalk.Model, 0, len(provider.Models))
		added := false
		for _, model := range provider.Models {
			if retiredDeepSeekFlash(model.ID) || model.ID == "deepseek-flash" {
				if added {
					continue
				}
				if current != nil {
					model = *current
				} else {
					// DeepSeek's canonical Flash alias serves V4.1-Flash-1017.
					// These are the published peak rates as of 2026-10-24.
					model.ID = "deepseek-flash"
					model.Name = "DeepSeek V4.1 Flash"
					model.ContextWindow = 1_000_000
					model.DefaultMaxTokens = 384_000
					model.CostPer1MIn = 0.3
					model.CostPer1MOut = 1.2
					model.CostPer1MInCached = 0.006
					model.CostPer1MOutCached = 0
					model.CanReason = true
					model.SupportsImages = true
				}
				added = true
			}
			model = withDeepSeekEffort(model)
			models = append(models, model)
		}
		provider.Models = models
		if retiredDeepSeekFlash(provider.DefaultLargeModelID) {
			provider.DefaultLargeModelID = "deepseek-flash"
		}
		if retiredDeepSeekFlash(provider.DefaultSmallModelID) {
			provider.DefaultSmallModelID = "deepseek-flash"
		}
		result[i] = provider
	}
	return result
}

// normalizeProviderEfforts fills in DeepSeek reasoning metadata for models
// declared on a provider config. Custom providers never pass through the
// catalog path, so without this they expose reasoning without any effort
// levels and the TUI hides the effort commands.
func normalizeProviderEfforts(provider ProviderConfig) ProviderConfig {
	provider.Models = slices.Clone(provider.Models)
	for i, model := range provider.Models {
		if !isDeepSeekReasoningModel(model.ID) {
			continue
		}
		provider.Models[i] = withDeepSeekEffort(model)
	}
	return provider
}

// withDeepSeekEffort applies DeepSeek's thinking-mode effort levels when
// the catalog does not provide them. DeepSeek accepts low, high, and max,
// with high as the documented default; none disables thinking mode.
func withDeepSeekEffort(model catwalk.Model) catwalk.Model {
	if !model.CanReason && !isDeepSeekReasoningModel(model.ID) {
		return model
	}
	model.CanReason = true
	if len(model.ReasoningLevels) == 0 {
		model.ReasoningLevels = []string{"low", "high", "max", "none"}
	}
	if model.DefaultReasoningEffort == "" {
		model.DefaultReasoningEffort = "high"
	}
	return model
}

func isDeepSeekReasoningModel(id string) bool {
	id = strings.ToLower(id)
	return strings.HasPrefix(id, "deepseek-flash") ||
		strings.HasPrefix(id, "deepseek-v4") ||
		strings.Contains(id, "reasoner")
}

func retiredDeepSeekFlash(id string) bool {
	return id == "deepseek-v4-flash" || id == "deepseek-v4-flash-vision-exp"
}

// canonicalizeDeepSeekSelection preserves Flash intent when migrating saved
// selections, instead of falling back to the provider's default Pro model.
func canonicalizeDeepSeekSelection(cfg *Config, selected *SelectedModel) bool {
	if selected.Provider != string(catwalk.InferenceProviderDeepSeek) || !retiredDeepSeekFlash(selected.Model) || cfg.GetModel(selected.Provider, "deepseek-flash") == nil {
		return false
	}
	selected.Model = "deepseek-flash"
	return true
}
