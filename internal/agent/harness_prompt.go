package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// harnessStateEnv overrides the default harness state location. It is
// used by tests and by embedders that keep the harness state outside
// the working directory.
const harnessStateEnv = "CRUSH_HARNESS_FILE"

// appendHarnessPrompt appends the rendered section of a saved harness
// state (see internal/harness) to the coder's system prompt. The state
// lives at .watcher/harness_state.json in the working directory, so a
// probe running in a git worktree picks up the candidate harness
// written there, and the main workspace picks up the refined harness
// on the next run. Missing or malformed files are silently ignored:
// the harness is an optimization layer, never a hard dependency.
func appendHarnessPrompt(systemPrompt, workingDir string) string {
	path := os.Getenv(harnessStateEnv)
	if path == "" {
		path = filepath.Join(workingDir, ".watcher", "harness_state.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return systemPrompt
	}
	var state struct {
		RenderedSystemPrompt string `json:"rendered_system_prompt"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return systemPrompt
	}
	section := strings.TrimSpace(state.RenderedSystemPrompt)
	if section == "" {
		return systemPrompt
	}
	return systemPrompt + "\n\n" + section
}
