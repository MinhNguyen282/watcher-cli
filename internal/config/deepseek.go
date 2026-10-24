package config

import (
	"slices"

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
