package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
)

const (
	// recentlyViewedFileLimit is how many of the most recently read files
	// are re-read from disk after compaction. Long files are cut short so
	// the working set stays smaller than the window it replaced.
	recentlyViewedFileLimit = 5
	// workingSetFileBudget bounds each re-read file's contribution.
	workingSetFileBudget = 8 * 1024
	// microcompactKeepRecent is how many trailing messages keep their full
	// tool output when the history is microcompacted.
	microcompactKeepRecent = 10
	// microcompactMinContent is the tool-output size below which pruning is
	// pointless and would only add noise.
	microcompactMinContent = 2 * 1024
)

// workingSetFile is one file carried across a compaction.
type workingSetFile struct {
	Path      string
	Content   string
	Truncated bool
}

// recentlyViewedFiles returns the file paths the conversation read, most
// recent first, without duplicates. It reads the tool calls rather than a
// tracker so it works on a resumed session whose tracker is empty.
func recentlyViewedFiles(msgs []message.Message, limit int) []string {
	if limit <= 0 {
		return nil
	}
	seen := make(map[string]bool)
	var files []string
	for i := len(msgs) - 1; i >= 0 && len(files) < limit; i-- {
		for _, call := range msgs[i].ToolCalls() {
			if call.Name != "view" {
				continue
			}
			var params struct {
				FilePath string `json:"file_path"`
			}
			if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
				continue
			}
			path := strings.TrimSpace(params.FilePath)
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			files = append(files, path)
			if len(files) >= limit {
				break
			}
		}
	}
	return files
}

// readWorkingSetFile re-reads a file from disk, cutting long files so the
// working set cannot reintroduce the pressure compaction just removed.
func readWorkingSetFile(path string) (workingSetFile, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return workingSetFile{}, false
	}
	content := string(data)
	truncated := false
	if len(content) > workingSetFileBudget {
		content = content[:workingSetFileBudget]
		truncated = true
	}
	return workingSetFile{Path: path, Content: content, Truncated: truncated}, true
}

// workingDir returns the session's working directory, falling back to the
// repository root of the process when no config store is available (tests,
// embedders).
func (a *sessionAgent) workingDir() string {
	if a.cfg != nil {
		if dir := a.cfg.WorkingDir(); dir != "" {
			return dir
		}
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}

// buildWorkingSetMessage renders what survives a compaction next to the
// summary: the plan, the files the session actually read, and where the full
// transcript lives. It is a hidden user message so it reaches the model
// without appearing as something the user typed.
func buildWorkingSetMessage(msgs []message.Message, workingDir, transcriptPath string) string {
	var b strings.Builder
	b.WriteString("Working set reconstructed after compaction. This is application context, not a user message.\n")

	files := recentlyViewedFiles(msgs, recentlyViewedFileLimit)
	included := make([]workingSetFile, 0, len(files))
	for _, path := range files {
		resolved := path
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(workingDir, path)
		}
		if file, ok := readWorkingSetFile(resolved); ok {
			included = append(included, file)
		}
	}
	if len(included) > 0 {
		b.WriteString("\nFiles read most recently (re-read from disk):\n")
		for _, file := range included {
			fmt.Fprintf(&b, "\n<file path=%q>\n%s\n", file.Path, file.Content)
			if file.Truncated {
				b.WriteString("[truncated]\n")
			}
			b.WriteString("</file>\n")
		}
	}
	if transcriptPath != "" {
		fmt.Fprintf(&b, "\nFull transcript: %s\n", transcriptPath)
	}
	return b.String()
}

// microcompactMessages prunes tool output that is no longer load-bearing.
// The newest messages keep their content; older tool results are replaced by
// a short marker so the model knows the call happened and can re-run it.
func microcompactMessages(msgs []fantasy.Message, keepRecent int) ([]fantasy.Message, int) {
	if keepRecent < 0 {
		keepRecent = 0
	}
	cutoff := len(msgs) - keepRecent
	if cutoff <= 0 {
		return msgs, 0
	}
	pruned := 0
	out := make([]fantasy.Message, len(msgs))
	copy(out, msgs)
	for i := 0; i < cutoff; i++ {
		parts := make([]fantasy.MessagePart, len(out[i].Content))
		copy(parts, out[i].Content)
		changed := false
		for j, part := range parts {
			result, ok := part.(fantasy.ToolResultPart)
			if !ok {
				continue
			}
			text, ok := result.Output.(fantasy.ToolResultOutputContentText)
			if !ok || len(text.Text) < microcompactMinContent {
				continue
			}
			result.Output = fantasy.ToolResultOutputContentText{
				Text: fmt.Sprintf("[tool output omitted by microcompaction: %d chars; call the tool again if the content is needed]", len(text.Text)),
			}
			parts[j] = result
			changed = true
			pruned++
		}
		if changed {
			out[i].Content = parts
		}
	}
	return out, pruned
}
