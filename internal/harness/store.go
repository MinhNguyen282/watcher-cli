package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DirName is the directory the harness keeps its state in, relative to
// the repository root.
const DirName = ".watcher"

const (
	harnessFileName = "harness_state.json"
	lessonsFileName = "lessons.md"
	lessonsDirName  = "memory"
	tracesDirName   = "traces"
	scriptsDirName  = "scripts"
)

// Store persists harness state under .watcher/ in a repository. All
// writes use 0o644/0o755 permissions and never touch tracked files, so
// the harness state stays invisible to `git diff` until the user
// chooses to commit it.
type Store struct {
	WorkingDir string
}

// NewStore returns a store rooted at the given repository directory.
func NewStore(workingDir string) *Store {
	return &Store{WorkingDir: workingDir}
}

func (s *Store) dir() string {
	return filepath.Join(s.WorkingDir, DirName)
}

// LoadHarness loads .watcher/harness_state.json. A missing file yields
// a default, empty harness (not an error) so repositories without
// prior evolution work out of the box.
func (s *Store) LoadHarness() (*HarnessState, error) {
	path := filepath.Join(s.dir(), harnessFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultHarness(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read harness state: %w", err)
	}
	var h HarnessState
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}
	return h.Sanitize(), nil
}

// SaveHarness writes the harness to .watcher/harness_state.json,
// refreshing the rendered system prompt section first.
func (s *Store) SaveHarness(h *HarnessState) error {
	if h == nil {
		return fmt.Errorf("cannot save a nil harness")
	}
	h = h.Sanitize()
	h.RefreshRendered()
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode harness state: %w", err)
	}
	return s.writeFile(harnessFileName, data, 0o644)
}

// LoadLessons returns the contents of .watcher/memory/lessons.md, or
// an empty string when no lessons exist yet.
func (s *Store) LoadLessons() (string, error) {
	path := filepath.Join(s.dir(), lessonsDirName, lessonsFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to read lessons: %w", err)
	}
	return string(data), nil
}

// AppendLesson adds one lesson to .watcher/memory/lessons.md.
func (s *Store) AppendLesson(lesson string) error {
	lesson = strings.TrimSpace(lesson)
	if lesson == "" {
		return nil
	}
	existing, err := s.LoadLessons()
	if err != nil {
		return err
	}
	entry := "## " + lesson
	if existing == "" {
		entry = "# Harness lessons\n\n" + entry
	} else {
		entry = existing + "\n\n" + entry
	}
	return s.writeFile(
		filepath.Join(lessonsDirName, lessonsFileName),
		[]byte(entry+"\n"),
		0o644,
	)
}

// SaveTrace persists one execution trace under .watcher/traces/.
func (s *Store) SaveTrace(t *ExecutionTrace) error {
	if t == nil {
		return fmt.Errorf("cannot save a nil trace")
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode trace: %w", err)
	}
	name := sanitizeFileName(t.ProbeID) + ".json"
	return s.writeFile(filepath.Join(tracesDirName, name), data, 0o644)
}

// WriteScripts materializes the harness helper scripts under
// .watcher/scripts/ in the given directory.
func (s *Store) WriteScripts(h *HarnessState) error {
	if h == nil || len(h.Scripts) == 0 {
		return nil
	}
	dir := filepath.Join(s.dir(), scriptsDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create scripts directory: %w", err)
	}
	for name, content := range h.Scripts {
		clean := sanitizeFileName(name)
		if clean == "" || clean == "." || clean == ".." || strings.ContainsAny(name, `/\`) {
			continue
		}
		path := filepath.Join(dir, clean)
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			return fmt.Errorf("failed to write script %s: %w", path, err)
		}
	}
	return nil
}

func (s *Store) writeFile(rel string, data []byte, perm os.FileMode) error {
	dir := s.dir()
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(rel)), 0o755); err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}
	path := filepath.Join(dir, rel)
	if err := os.WriteFile(path, data, perm); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

// DefaultHarness returns an empty harness with a stable ID.
func DefaultHarness() *HarnessState {
	return &HarnessState{ID: "base"}
}

// sanitizeFileName maps a value to a safe file name fragment.
func sanitizeFileName(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		case r == '-', r == '_', r == '.':
			sb.WriteRune(r)
		default:
			sb.WriteRune('-')
		}
	}
	return strings.Trim(sb.String(), "-")
}
