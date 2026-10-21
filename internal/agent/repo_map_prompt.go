package agent

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/repomap"
)

// appendRepoMapPrompt appends a bounded summary of the repository map
// to the coder's system prompt, so the first turn already knows the
// codebase layout instead of grepping for it. Loading is bounded by
// repoMapLoadTimeout: on a miss the build is retried in the background
// and the prompt stays unchanged, because the map is a navigation aid,
// never a hard dependency.
func appendRepoMapPrompt(ctx context.Context, systemPrompt string, svc *repomap.Service) string {
	if svc == nil {
		return systemPrompt
	}
	loadCtx, cancel := context.WithTimeout(ctx, repoMapLoadTimeout)
	defer cancel()

	g, err := svc.Load(loadCtx)
	if err != nil || g == nil {
		go func() {
			if _, err := svc.Build(context.Background()); err != nil {
				slog.Warn("Repository map background build failed", "error", err)
			}
		}()
		return systemPrompt
	}
	section := svc.PromptSection(g)
	if strings.TrimSpace(section) == "" {
		return systemPrompt
	}
	return systemPrompt + "\n\n" + section
}

// repoMapLoadTimeout bounds the synchronous map load during agent
// construction. Repositories that take longer warm their cache in the
// background for the next run.
const repoMapLoadTimeout = 5 * time.Second
