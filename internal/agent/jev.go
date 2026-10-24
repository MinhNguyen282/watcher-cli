package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/jevrouting"
	"github.com/charmbracelet/crush/internal/pubsub"
)

func (c *coordinator) observeJev(ctx context.Context, sessionID, prompt string, current config.SelectedModel, agentName string) {
	cfg := c.cfg.Config()
	if cfg.Options == nil || !cfg.Options.JevObserve {
		return
	}
	in := jevrouting.Input{
		Prompt:       prompt,
		Models:       make(map[string]string),
		Skills:       map[string]string{"agent_decides": "Let the coding agent choose skills as usual."},
		DefaultModel: string(cfg.Agents[agentName].Model),
	}
	for tier, model := range cfg.Models {
		if model.Model == "" || model.Provider == "" {
			continue
		}
		provider, ok := cfg.Providers.Get(model.Provider)
		if !ok || provider.Disable {
			continue
		}
		in.Models[string(tier)] = model.Provider + "/" + model.Model
	}
	// The current agent's actual model is authoritative for the fallback.
	in.Models[in.DefaultModel] = current.Provider + "/" + current.Model
	for i, skill := range c.activeSkills {
		if skill.DisableModelInvocation {
			continue
		}
		// Opaque IDs prevent skill names from colliding with the fallback.
		in.Skills[fmt.Sprintf("skill_%d", i)] = skill.Name + ": " + skill.Description
	}
	started := time.Now()
	observation, err := jevrouting.Observe(ctx, &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, jevrouting.Endpoint, os.Getenv("TYPESAFE_API_KEY"), cfg.Options.JevModel, in)
	text := "Jev observe unavailable; continuing with current model and agent skill selection."
	if err != nil {
		slog.Warn("Jev observation unavailable", "session_id", sessionID, "run_id", RunIDFromContext(ctx), "latency", time.Since(started), "error", err)
	} else {
		text = observation.Summary()
		if description, ok := in.Skills[observation.Answers["skill"].Choice]; ok {
			text += " [" + description + "]"
		}
		slog.Info("Jev routing observation", "session_id", sessionID, "run_id", RunIDFromContext(ctx),
			"latency", time.Since(started), "observation", observation, "models", in.Models, "skills", in.Skills)
	}
	if c.notify != nil {
		c.notify.Publish(pubsub.CreatedEvent, notify.Notification{
			SessionID: sessionID, RunID: RunIDFromContext(ctx), Type: notify.TypeRoutingObservation, RoutingSummary: text,
		})
	}
}
