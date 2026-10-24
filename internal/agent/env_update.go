package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
)

// envSnapshot is the cheap subset of the environment the agent re-checks
// while a session runs. Git status is deliberately excluded: it costs a
// subprocess per step, so it stays a snapshot from prompt build time and the
// model is told so.
type envSnapshot struct {
	WorkingDir string
	IsGitRepo  bool
	Platform   string
	Date       string
}

func snapshotEnv(cfg *config.ConfigStore) (envSnapshot, bool) {
	if cfg == nil {
		return envSnapshot{}, false
	}
	dir := cfg.WorkingDir()
	if dir == "" {
		return envSnapshot{}, false
	}
	return envSnapshot{
		WorkingDir: filepath.ToSlash(dir),
		IsGitRepo:  isGitDir(dir),
		Platform:   runtime.GOOS,
		Date:       time.Now().Format(prompt.DateFormat),
	}, true
}

func isGitDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// envChanges lists the fields that differ between two snapshots, using the
// names the environment record uses so the update reads naturally.
func envChanges(prev, next envSnapshot) []string {
	var changed []string
	if prev.WorkingDir != next.WorkingDir {
		changed = append(changed, "the working directory")
	}
	if prev.IsGitRepo != next.IsGitRepo {
		changed = append(changed, "whether the directory is a git repository")
	}
	if prev.Platform != next.Platform {
		changed = append(changed, "the platform")
	}
	if prev.Date != next.Date {
		changed = append(changed, "the date")
	}
	return changed
}

// environmentUpdate returns a replacement notice when the environment
// changed since the last time this session was told about it. The first call
// only records the baseline: the environment is already in the system
// prompt.
func (a *sessionAgent) environmentUpdate(sessionID string) (string, bool) {
	current, ok := snapshotEnv(a.cfg)
	if !ok {
		return "", false
	}
	previous, seen := a.envStates.Get(sessionID)
	if !seen {
		a.envStates.Set(sessionID, current)
		return "", false
	}
	changed := envChanges(previous, current)
	if len(changed) == 0 {
		return "", false
	}
	a.envStates.Set(sessionID, current)
	block := prompt.EnvironmentUpdate(prompt.PromptDat{
		WorkingDir: current.WorkingDir,
		IsGitRepo:  current.IsGitRepo,
		Platform:   current.Platform,
		Date:       current.Date,
	}, changed)
	return block.Text, block.Text != ""
}
