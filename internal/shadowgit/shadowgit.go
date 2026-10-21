// Package shadowgit provides hidden git-backed checkpoints of the
// working tree, so agent file mutations can be rewound ("time travel")
// without touching the repository's real history.
//
// The shadow repository lives at .watcher/shadow-git/ and is a normal
// git repository whose work tree is the project root. Every snapshot is
// a real commit (so diffs and logs work), but the repository is
// invisible to the main .git: .watcher/ is appended to the project's
// .gitignore on init, and .git/.watcher are excluded from the shadow
// index.
package shadowgit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DirName is the directory that holds the shadow repository, relative
// to the repository root (under the harness' .watcher/ directory).
const DirName = "shadow-git"

// Checkpoint is one snapshot of the working tree. When Noop is true no
// new commit was created because the tree was already identical to the
// previous snapshot; GitCommit then points at that previous snapshot.
type Checkpoint struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"session_id"`
	MessageID  string    `json:"message_id"`
	ToolCallID string    `json:"tool_call_id"`
	ToolName   string    `json:"tool_name"`
	GitCommit  string    `json:"git_commit"`
	Files      []string  `json:"files"`
	CreatedAt  time.Time `json:"created_at"`
	Noop       bool      `json:"noop"`
}

// Meta identifies the agent activity that triggered a snapshot.
type Meta struct {
	SessionID  string
	MessageID  string
	ToolCallID string
	ToolName   string
}

// RestoreResult reports the outcome of a Restore call.
type RestoreResult struct {
	Restored   bool
	FromCommit string
	// Files lists the tracked files that changed between the state
	// before the restore and the restored state.
	Files []string
	// SafetyCheckpointID is non-empty when the working tree was dirty
	// before restoring: the dirty state was snapshotted as a "rewind
	// guard" checkpoint so the user can restore back to it at any
	// time. Restoring to that checkpoint brings the changes back.
	SafetyCheckpointID string
}

// Service snapshots and restores working tree states.
type Service interface {
	// Enabled reports whether shadow checkpoints are active. It is
	// false when the working directory is not a git repository or the
	// shadow repository could not be created.
	Enabled() bool

	// Snapshot commits the current working tree state (when it changed
	// since the last snapshot) and returns the checkpoint. A clean
	// tree yields a Noop checkpoint pointing at the existing commit.
	Snapshot(ctx context.Context, meta Meta) (Checkpoint, error)

	// Restore rewinds the working tree to the checkpoint's commit. A
	// dirty tree is snapshotted first as a "rewind guard" checkpoint,
	// whose ID is reported so the user can restore back to it.
	Restore(ctx context.Context, sessionID, checkpointID string) (RestoreResult, error)

	// List returns the session's checkpoints in chronological order.
	List(ctx context.Context, sessionID string) ([]Checkpoint, error)

	// Fork creates a branch in the shadow repository at the
	// checkpoint's commit and returns the branch name.
	Fork(ctx context.Context, checkpointID, branchSuffix string) (string, error)

	// DiffStat renders a short diffstat between the checkpoint and the
	// current HEAD of the shadow repository. Used for UI previews.
	DiffStat(ctx context.Context, checkpointID string) (string, error)
}

type service struct {
	workingDir string
	enabled    bool
	mu         sync.Mutex
}

// New returns a checkpoint service for the given repository directory.
// A non-git directory yields a disabled service, never an error.
func New(workingDir string) (Service, error) {
	if _, err := runGitOut(workingDir, "rev-parse", "--git-dir"); err != nil {
		slog.Info("Shadow git disabled: working directory is not a git repository",
			"dir", workingDir, "error", err)
		return &service{workingDir: workingDir}, nil
	}
	return &service{workingDir: workingDir, enabled: true}, nil
}

func (s *service) Enabled() bool {
	return s.enabled
}

func (s *service) gitDir() string {
	return filepath.Join(s.workingDir, ".watcher", DirName)
}

// initShadowRepo creates the shadow repository, its excludes, and
// appends .watcher/ to the project's .gitignore when missing. It is
// idempotent.
func (s *service) initShadowRepo() error {
	dir := s.gitDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create shadow git directory: %w", err)
	}
	// The shadow repo carries its own excludes so the main repository
	// metadata (.git) and the harness state (.watcher) are never
	// snapshotted, even if the project .gitignore is missing entries.
	excludePath := filepath.Join(dir, ".git", "info", "exclude")
	if _, err := os.Stat(excludePath); errors.Is(err, os.ErrNotExist) {
		if _, err := runGitOut(s.workingDir, "init", "--quiet", "--initial-branch=main", dir); err != nil {
			return fmt.Errorf("failed to init shadow repository: %w", err)
		}
		// Use a stable machine identity so bookkeeping commits work
		// even when the user has not configured a git identity.
		for key, value := range map[string]string{
			"user.name":           "Crush Shadow",
			"user.email":          "shadow@crush.local",
			"commit.gpgSign":      "false",
			"tag.gpgSign":         "false",
			"core.hooksPath":      filepath.Join(dir, ".git", "hooks-none"),
			"core.autocrlf":       "false",
			"core.safecrlf":       "false",
			"core.filemode":       "true",
			"log.showSignature":   "false",
			"advice.detachedHead": "false",
		} {
			if err := s.git("config", key, value); err != nil {
				return fmt.Errorf("failed to configure shadow repository: %w", err)
			}
		}
		excludes := ".git\n.watcher\n"
		if err := os.WriteFile(excludePath, []byte(excludes), 0o644); err != nil {
			return fmt.Errorf("failed to write shadow excludes: %w", err)
		}
	}
	if err := s.ensureGitignore(); err != nil {
		return err
	}
	return nil
}

// ensureGitignore appends .watcher/ to the project .gitignore so the
// shadow repository stays invisible to the real repository.
func (s *service) ensureGitignore() error {
	path := filepath.Join(s.workingDir, ".gitignore")
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return os.WriteFile(path, []byte(".watcher/\n"), 0o644)
	case err != nil:
		return fmt.Errorf("failed to read .gitignore: %w", err)
	}
	content := string(data)
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == ".watcher/" || strings.TrimSpace(line) == ".watcher" {
			return nil
		}
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += ".watcher/\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to update .gitignore: %w", err)
	}
	return nil
}

// git runs a git command against the shadow repository, whose work
// tree is the project root.
func (s *service) git(args ...string) error {
	_, err := s.gitOut(args...)
	return err
}

func (s *service) gitOut(args ...string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	full := append([]string{
		"--git-dir=" + filepath.Join(s.gitDir(), ".git"),
		"--work-tree=" + s.workingDir,
	}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = s.workingDir
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "),
				strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// dirty reports whether the shadow working tree has changes.
func (s *service) dirty() (bool, error) {
	out, err := s.gitOut("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (s *service) Snapshot(ctx context.Context, meta Meta) (Checkpoint, error) {
	cp := Checkpoint{
		SessionID:  meta.SessionID,
		MessageID:  meta.MessageID,
		ToolCallID: meta.ToolCallID,
		ToolName:   meta.ToolName,
	}
	if !s.enabled || meta.SessionID == "" {
		cp.Noop = true
		return cp, nil
	}
	if err := ctx.Err(); err != nil {
		return cp, err
	}
	if err := s.initShadowRepo(); err != nil {
		slog.Warn("Shadow git snapshot skipped: failed to initialize shadow repository",
			"error", err)
		cp.Noop = true
		return cp, nil
	}

	changed, err := s.dirty()
	if err != nil {
		return cp, fmt.Errorf("failed to check shadow status: %w", err)
	}
	head, err := s.gitOut("rev-parse", "--verify", "HEAD")
	if err != nil {
		// A repository without commits has no HEAD yet; treat it as an
		// empty head so the first snapshot can commit everything.
		if !strings.Contains(err.Error(), "Needed a single revision") {
			return cp, fmt.Errorf("failed to resolve shadow HEAD: %w", err)
		}
		head = ""
	}
	head = strings.TrimSpace(head)

	if !changed {
		cp.Noop = true
		cp.GitCommit = head
		cp.ID = shortSHA(head)
		cp.CreatedAt = s.commitTime(head)
		return cp, nil
	}

	if err := s.git("add", "-A"); err != nil {
		return cp, fmt.Errorf("failed to stage shadow snapshot: %w", err)
	}
	msg := fmt.Sprintf("checkpoint session=%s message=%s tool=%s call=%s",
		meta.SessionID, meta.MessageID, meta.ToolName, meta.ToolCallID)
	commitErr := s.git("commit", "--no-verify", "--no-gpg-sign", "-m", msg)
	if commitErr != nil && !strings.Contains(commitErr.Error(), "nothing to commit") {
		return cp, fmt.Errorf("failed to commit shadow snapshot: %w", commitErr)
	}
	head, err = s.gitOut("rev-parse", "HEAD")
	if err != nil {
		return cp, fmt.Errorf("failed to resolve shadow HEAD after snapshot: %w", err)
	}
	head = strings.TrimSpace(head)
	cp.GitCommit = head
	cp.ID = shortSHA(head)
	cp.CreatedAt = s.commitTime(head)
	cp.Files, err = s.commitFiles(head)
	if err != nil {
		slog.Warn("Shadow git: failed to list snapshot files", "error", err)
	}
	return cp, nil
}

// commitTime resolves the commit's author time.
func (s *service) commitTime(sha string) time.Time {
	out, err := s.gitOut("show", "-s", "--format=%ct", sha)
	if err != nil {
		return time.Time{}
	}
	secs, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(secs, 0)
}

// commitFiles resolves the files changed by a commit.
func (s *service) commitFiles(sha string) ([]string, error) {
	out, err := s.gitOut("show", "--name-only", "--format=", sha)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

func (s *service) Restore(ctx context.Context, sessionID, checkpointID string) (RestoreResult, error) {
	var res RestoreResult
	if !s.enabled {
		return res, fmt.Errorf("shadow git is not enabled in this directory")
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	sha, err := s.resolveCheckpoint(sessionID, checkpointID)
	if err != nil {
		return res, err
	}

	before, err := s.gitOut("rev-parse", "HEAD")
	if err != nil {
		return res, fmt.Errorf("failed to resolve shadow HEAD: %w", err)
	}
	before = strings.TrimSpace(before)

	changed, err := s.dirty()
	if err != nil {
		return res, err
	}
	if changed {
		// Preserve the user's in-flight changes as a checkpoint so the
		// rewind is never destructive: restoring back to that guard
		// checkpoint brings the dirty state back.
		guard, err := s.Snapshot(ctx, Meta{SessionID: sessionID, ToolName: "rewind-guard"})
		if err != nil {
			slog.Warn("Shadow git: failed to snapshot dirty tree before rewind",
				"error", err)
		} else {
			res.SafetyCheckpointID = guard.ID
		}
	}

	if err := s.git("checkout", "-f", sha, "--", "."); err != nil {
		return res, fmt.Errorf("failed to restore working tree: %w", err)
	}

	out, err := s.gitOut("diff", "--name-only", before, sha)
	if err == nil {
		for _, line := range strings.Split(out, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				res.Files = append(res.Files, line)
			}
		}
	}
	res.Restored = true
	res.FromCommit = sha
	return res, nil
}

// resolveCheckpoint maps a checkpoint ID (short SHA or commit SHA) to a
// full commit that belongs to the given session.
func (s *service) resolveCheckpoint(sessionID, checkpointID string) (string, error) {
	checkpoints, err := s.List(context.Background(), sessionID)
	if err != nil {
		return "", err
	}
	for _, cp := range checkpoints {
		if cp.ID == checkpointID || cp.GitCommit == checkpointID ||
			strings.HasPrefix(cp.GitCommit, checkpointID) {
			return cp.GitCommit, nil
		}
	}
	return "", fmt.Errorf("checkpoint %q not found for session %s", checkpointID, sessionID)
}

func (s *service) List(ctx context.Context, sessionID string) ([]Checkpoint, error) {
	var out []Checkpoint
	if !s.enabled {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The log greps for the session marker in commit messages. Newest
	// first; reversed into chronological order below.
	raw, err := s.gitOut("log", "--format=%H%x00%s%x00%ct",
		"--grep=session="+sessionID)
	if err != nil {
		// A fresh shadow repository has no commits yet.
		if strings.Contains(err.Error(), "does not have any commits") {
			return out, nil
		}
		return nil, fmt.Errorf("failed to list shadow checkpoints: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		parts := strings.SplitN(line, "\x00", 3)
		if len(parts) != 3 {
			continue
		}
		cp := Checkpoint{
			ID:        shortSHA(parts[0]),
			GitCommit: parts[0],
			SessionID: sessionID,
		}
		if secs, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
			cp.CreatedAt = time.Unix(secs, 0)
		}
		cp.MessageID, cp.ToolCallID, cp.ToolName = parseCommitSubject(parts[1])
		out = append(out, cp)
	}
	// Chronological order for the UI.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// parseCommitSubject extracts the metadata fields from a checkpoint
// commit message.
func parseCommitSubject(subject string) (messageID, toolCallID, toolName string) {
	for _, field := range strings.Fields(subject) {
		switch {
		case strings.HasPrefix(field, "message="):
			messageID = strings.TrimPrefix(field, "message=")
		case strings.HasPrefix(field, "call="):
			toolCallID = strings.TrimPrefix(field, "call=")
		case strings.HasPrefix(field, "tool="):
			toolName = strings.TrimPrefix(field, "tool=")
		}
	}
	return messageID, toolCallID, toolName
}

func (s *service) Fork(ctx context.Context, checkpointID, branchSuffix string) (string, error) {
	if !s.enabled {
		return "", fmt.Errorf("shadow git is not enabled in this directory")
	}
	sha, err := s.gitOut("rev-parse", "--verify", checkpointID)
	if err != nil {
		return "", fmt.Errorf("failed to resolve checkpoint %q: %w", checkpointID, err)
	}
	branch := "shadow-fork-" + sanitize(branchSuffix)
	if err := s.git("branch", branch, strings.TrimSpace(sha)); err != nil {
		return "", fmt.Errorf("failed to create shadow fork branch: %w", err)
	}
	return branch, nil
}

func (s *service) DiffStat(ctx context.Context, checkpointID string) (string, error) {
	if !s.enabled {
		return "", fmt.Errorf("shadow git is not enabled in this directory")
	}
	sha, err := s.gitOut("rev-parse", "--verify", checkpointID)
	if err != nil {
		return "", fmt.Errorf("failed to resolve checkpoint %q: %w", checkpointID, err)
	}
	out, err := s.gitOut("diff", "--stat", strings.TrimSpace(sha), "HEAD")
	if err != nil {
		return "", fmt.Errorf("failed to diff checkpoint: %w", err)
	}
	return strings.TrimRight(out, "\n"), nil
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// sanitize maps a value to a safe branch name fragment.
func sanitize(s string) string {
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
	out := strings.Trim(sb.String(), "-")
	if out == "" {
		out = "fork"
	}
	return out
}

// runGitOut runs a plain git command in dir (used before the shadow
// repository exists, e.g. the git-repo check in New).
func runGitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "),
				strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
