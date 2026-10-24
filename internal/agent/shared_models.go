package agent

import (
	"context"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
)

// ModelSet is the large and small model an agent sends requests through.
type ModelSet struct {
	Large, Small Model
}

// coordinatorModels holds each configured model slot and its delegated build.
// Copilot uses a separate build so its requests keep the agent-initiated
// billing marker.
type coordinatorModels struct {
	primary  map[config.SelectedModelType]Model
	subAgent map[config.SelectedModelType]Model
}

// rebuildModels rebuilds the configured model slots and publishes them
// together. Rebuilds are serialized so an older config snapshot cannot publish
// after a newer credential refresh.
func (c *coordinator) rebuildModels(ctx context.Context) error {
	c.rebuildMu.Lock()
	defer c.rebuildMu.Unlock()

	configured := c.cfg.Config().Models
	next := coordinatorModels{
		primary:  make(map[config.SelectedModelType]Model, len(configured)),
		subAgent: make(map[config.SelectedModelType]Model, len(configured)),
	}
	for slot := range configured {
		primary, err := c.buildModel(ctx, slot, false)
		if err != nil {
			return err
		}
		subAgent := primary
		if primary.ModelCfg.Provider == string(catwalk.InferenceProviderCopilot) {
			subAgent, err = c.buildModel(ctx, slot, true)
			if err != nil {
				return err
			}
		}
		next.primary[slot] = primary
		next.subAgent[slot] = subAgent
	}
	c.models.Set(next)
	return nil
}

func (c *coordinator) modelsFor(slot config.SelectedModelType, isSubAgent bool) func() ModelSet {
	if slot == config.SelectedModelTypeFast {
		if _, ok := c.models.Get().primary[slot]; !ok {
			slot = config.SelectedModelTypeSmall
		}
	}
	return func() ModelSet {
		models := c.models.Get()
		primary := models.primary[slot]
		if isSubAgent {
			primary = models.subAgent[slot]
		}
		return ModelSet{
			Large: primary,
			Small: models.subAgent[config.SelectedModelTypeSmall],
		}
	}
}

// smallOnlyModels serves agents that use the small model for everything.
func (c *coordinator) smallOnlyModels() ModelSet {
	return c.modelsFor(config.SelectedModelTypeSmall, true)()
}

// currentBuildOf returns the latest build of the model a turn started with, so
// a retry gets a refreshed credential but a model switch waits for the next
// turn.
func (a *sessionAgent) currentBuildOf(started Model) fantasy.LanguageModel {
	m := a.models().Large
	if m.ModelCfg.Provider != started.ModelCfg.Provider || m.ModelCfg.Model != started.ModelCfg.Model {
		return started.Model
	}
	return m.Model
}
