package shell

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// MaxFinishedJobs bounds how many finished jobs keep their output
	// readable. The oldest are dropped first.
	MaxFinishedJobs = 250
	// CompletedJobRetention is how long a finished job's output stays
	// readable.
	CompletedJobRetention = 8 * time.Hour
	// BackgroundKillTimeout bounds cancellation and output-pipe cleanup.
	BackgroundKillTimeout = 10 * time.Second
	// MaxJobBufferBytes is how much of each output stream a job keeps.
	// Older bytes are dropped once the stream outgrows the ring, and
	// readers are told exactly how much was lost.
	MaxJobBufferBytes = 2 << 20
)

// syncBuffer is a thread-safe bounded ring of the most recent bytes
// written to it. Older bytes are overwritten, but the total byte count is
// kept so cursors can detect loss.
type syncBuffer struct {
	mu      sync.RWMutex
	limit   int
	buf     []byte
	start   int
	dropped int64
	total   int64
}

func newSyncBuffer(limit int) *syncBuffer {
	if limit <= 0 {
		limit = MaxJobBufferBytes
	}
	return &syncBuffer{limit: limit}
}

func (sb *syncBuffer) Write(p []byte) (n int, err error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	if sb.limit <= 0 {
		sb.limit = MaxJobBufferBytes
	}
	n = len(p)
	sb.total += int64(n)
	if len(p) >= sb.limit {
		sb.buf = append(sb.buf[:0], p[len(p)-sb.limit:]...)
		sb.start = 0
		sb.dropped = sb.total - int64(len(sb.buf))
		return n, nil
	}

	room := sb.limit - len(sb.buf)
	if room > 0 {
		take := min(room, len(p))
		sb.buf = append(sb.buf, p[:take]...)
		p = p[take:]
	}
	for _, b := range p {
		sb.buf[sb.start] = b
		sb.start = (sb.start + 1) % sb.limit
		sb.dropped++
	}
	return n, nil
}

func (sb *syncBuffer) WriteString(s string) (n int, err error) {
	return sb.Write([]byte(s))
}

// snapshotLocked returns the retained bytes in write order.
func (sb *syncBuffer) snapshotLocked() string {
	if len(sb.buf) < sb.limit {
		return string(sb.buf)
	}
	out := make([]byte, 0, len(sb.buf))
	out = append(out, sb.buf[sb.start:]...)
	out = append(out, sb.buf[:sb.start]...)
	return string(out)
}

func (sb *syncBuffer) String() string {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	return sb.snapshotLocked()
}

// readFrom returns the retained bytes at or after the absolute offset.
// The second return value is the absolute offset of the next unread byte.
// The third reports that offset predates the oldest retained byte. The
// fourth is the total number of bytes ever written.
func (sb *syncBuffer) readFrom(offset int64) (data string, next int64, truncated bool, total int64) {
	sb.mu.RLock()
	defer sb.mu.RUnlock()

	if offset < 0 {
		offset = 0
	}
	if offset < sb.dropped {
		truncated = true
		offset = sb.dropped
	}
	if offset > sb.total {
		offset = sb.total
	}
	retained := sb.snapshotLocked()
	skip := int(offset - sb.dropped)
	if skip > len(retained) {
		skip = len(retained)
	}
	return retained[skip:], sb.total, truncated, sb.total
}

// BackgroundShell represents a shell running in the background.
type BackgroundShell struct {
	ID          string
	SessionID   string
	Command     string
	Description string
	Shell       *Shell
	WorkingDir  string
	ctx         context.Context
	cancel      context.CancelFunc
	stdout      *syncBuffer
	stderr      *syncBuffer
	done        chan struct{}
	exitErr     error
	completedAt atomic.Int64 // Unix timestamp when job completed (0 if still running)

	outcomeMu  sync.RWMutex
	exitCode   int
	signal     string
	timedOut   bool
	aborted    bool
	killReason string

	cursorMu     sync.Mutex
	stdoutCursor int64
	stderrCursor int64
}

// Outcome describes how a job ended. Each field is an independent fact:
// a non-zero exit code does not imply a timeout, and a timeout does not
// erase the exit code a command reported first.
type Outcome struct {
	ExitCode   int    `json:"exit_code"`
	Signal     string `json:"signal,omitempty"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Aborted    bool   `json:"aborted,omitempty"`
	KillReason string `json:"kill_reason,omitempty"`
}

// Outcome returns the independent completion facts for the job. The
// result is only meaningful once the job is done.
func (bs *BackgroundShell) Outcome() Outcome {
	bs.outcomeMu.RLock()
	defer bs.outcomeMu.RUnlock()
	return Outcome{
		ExitCode:   bs.exitCode,
		Signal:     bs.signal,
		TimedOut:   bs.timedOut,
		Aborted:    bs.aborted,
		KillReason: bs.killReason,
	}
}

func (bs *BackgroundShell) setOutcome(err error) {
	bs.outcomeMu.Lock()
	defer bs.outcomeMu.Unlock()
	bs.exitCode = ExitCode(err)
	if err != nil {
		bs.signal = SignalName(err)
		bs.timedOut = errors.Is(err, context.DeadlineExceeded) || errors.Is(bs.ctx.Err(), context.DeadlineExceeded)
		bs.aborted = errors.Is(err, context.Canceled) || errors.Is(bs.ctx.Err(), context.Canceled)
	}
}

func (bs *BackgroundShell) setKillReason(reason string) {
	bs.outcomeMu.Lock()
	bs.killReason = reason
	bs.outcomeMu.Unlock()
}

// ReadFrom returns output written at or after the given absolute offsets.
// A zero offset means "use the cursor from the previous read"; pass
// fromStart to read the retained buffer from the beginning.
func (bs *BackgroundShell) ReadFrom(fromStart bool, offset int64) (
	stdout, stderr string,
	nextStdout, nextStderr int64,
	truncated, done bool,
	err error,
) {
	bs.cursorMu.Lock()
	defer bs.cursorMu.Unlock()

	if fromStart {
		bs.stdoutCursor = 0
		bs.stderrCursor = 0
	}
	if offset > 0 {
		bs.stdoutCursor = offset
		bs.stderrCursor = offset
	}

	stdout, nextStdout, stdoutTruncated, _ := bs.stdout.readFrom(bs.stdoutCursor)
	stderr, nextStderr, stderrTruncated, _ := bs.stderr.readFrom(bs.stderrCursor)
	bs.stdoutCursor = nextStdout
	bs.stderrCursor = nextStderr
	done = bs.IsDone()
	if done {
		err = bs.exitErr
	}
	return stdout, stderr, nextStdout, nextStderr, stdoutTruncated || stderrTruncated, done, err
}

// Cursors returns the absolute read positions for both streams.
func (bs *BackgroundShell) Cursors() (stdout, stderr int64) {
	bs.cursorMu.Lock()
	defer bs.cursorMu.Unlock()
	return bs.stdoutCursor, bs.stderrCursor
}

// BackgroundShellManager manages background shell instances.
type BackgroundShellManager struct {
	mu     sync.Mutex
	shells map[string]*BackgroundShell
}

var (
	backgroundManager     *BackgroundShellManager
	backgroundManagerOnce sync.Once
	idCounter             atomic.Uint64
)

// newBackgroundShellManager creates a new BackgroundShellManager instance.
func newBackgroundShellManager() *BackgroundShellManager {
	return &BackgroundShellManager{shells: map[string]*BackgroundShell{}}
}

// GetBackgroundShellManager returns the singleton background shell manager.
func GetBackgroundShellManager() *BackgroundShellManager {
	backgroundManagerOnce.Do(func() {
		backgroundManager = newBackgroundShellManager()
	})
	return backgroundManager
}

type startOptions struct {
	sessionID string
	buffer    int
}

// StartOption configures a background job.
type StartOption func(*startOptions)

// WithSessionID scopes a job to a session so other sessions cannot read
// or kill it.
func WithSessionID(sessionID string) StartOption {
	return func(o *startOptions) { o.sessionID = sessionID }
}

// WithBufferLimit overrides the retained bytes per output stream.
func WithBufferLimit(limit int) StartOption {
	return func(o *startOptions) { o.buffer = limit }
}

// Start creates and starts a new background shell with the given command,
// unless maxJobs jobs are already running.
func (m *BackgroundShellManager) Start(ctx context.Context, workingDir string, blockFuncs []BlockFunc, command, description string, maxJobs int, opts ...StartOption) (*BackgroundShell, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if running := m.prune(); running >= maxJobs {
		return nil, fmt.Errorf("maximum number of running background jobs (%d) reached. Please terminate or wait for some jobs to complete", maxJobs)
	}

	var cfg startOptions
	for _, opt := range opts {
		opt(&cfg)
	}

	id := fmt.Sprintf("%03X", idCounter.Add(1))

	shell := NewShell(&Options{
		WorkingDir: workingDir,
		BlockFuncs: blockFuncs,
	})

	shellCtx, cancel := context.WithCancel(ctx)

	bgShell := &BackgroundShell{
		ID:          id,
		SessionID:   cfg.sessionID,
		Command:     command,
		Description: description,
		WorkingDir:  workingDir,
		Shell:       shell,
		ctx:         shellCtx,
		cancel:      cancel,
		stdout:      newSyncBuffer(cfg.buffer),
		stderr:      newSyncBuffer(cfg.buffer),
		done:        make(chan struct{}),
	}

	m.shells[id] = bgShell

	go func() {
		defer close(bgShell.done)

		err := shell.ExecStream(shellCtx, command, bgShell.stdout, bgShell.stderr)

		bgShell.exitErr = err
		bgShell.setOutcome(err)
		bgShell.completedAt.Store(time.Now().Unix())
	}()

	return bgShell, nil
}

// prune drops finished jobs that are past retention or beyond
// MaxFinishedJobs, and reports how many jobs are still running. Callers
// must hold m.mu.
func (m *BackgroundShellManager) prune() (running int) {
	cutoff := time.Now().Add(-CompletedJobRetention).Unix()
	var finished []*BackgroundShell
	for id, shell := range m.shells {
		switch at := shell.completedAt.Load(); {
		case at == 0:
			running++
		case at < cutoff:
			delete(m.shells, id)
		default:
			finished = append(finished, shell)
		}
	}

	if over := len(finished) - MaxFinishedJobs; over > 0 {
		slices.SortFunc(finished, func(a, b *BackgroundShell) int {
			return cmp.Compare(a.completedAt.Load(), b.completedAt.Load())
		})
		for _, shell := range finished[:over] {
			delete(m.shells, shell.ID)
		}
	}
	return running
}

// take removes and returns the shell with the given ID.
func (m *BackgroundShellManager) take(id string) (*BackgroundShell, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	shell, ok := m.shells[id]
	if !ok {
		return nil, fmt.Errorf("background shell not found: %s", id)
	}
	delete(m.shells, id)
	return shell, nil
}

// Get retrieves a background shell by ID.
func (m *BackgroundShellManager) Get(id string) (*BackgroundShell, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	shell, ok := m.shells[id]
	return shell, ok
}

// GetOwned retrieves a background shell by ID and verifies the caller owns
// it. A job without an owner can be read by anyone; a job with an owner
// requires an exact session match. The second return value is false when
// the job exists but belongs to another session, so callers can report an
// ownership error instead of leaking another session's output.
func (m *BackgroundShellManager) GetOwned(id, sessionID string) (*BackgroundShell, bool, bool) {
	shell, ok := m.Get(id)
	if !ok {
		return nil, false, false
	}
	if shell.SessionID != "" && shell.SessionID != sessionID {
		return nil, true, false
	}
	return shell, true, true
}

// Remove stops tracking a background shell without terminating it.
func (m *BackgroundShellManager) Remove(id string) error {
	_, err := m.take(id)
	return err
}

// Kill terminates a background shell by ID.
func (m *BackgroundShellManager) Kill(id string) error {
	return m.KillContext(context.Background(), id)
}

// KillContext cancels a job and waits for bounded cleanup. A job that has not
// finished remains tracked so callers can inspect its output or retry.
func (m *BackgroundShellManager) KillContext(ctx context.Context, id string) error {
	shell, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("background shell not found: %s", id)
	}
	shell.setKillReason("kill requested")
	shell.cancel()
	ctx, cancel := context.WithTimeout(ctx, BackgroundKillTimeout)
	defer cancel()
	select {
	case <-shell.done:
	default:
		select {
		case <-shell.done:
		case <-ctx.Done():
			return fmt.Errorf("background shell %s cleanup did not finish; termination requested and job is still tracked: %w", id, ctx.Err())
		}
	}
	m.mu.Lock()
	if m.shells[id] == shell {
		delete(m.shells, id)
	}
	m.mu.Unlock()
	return nil
}

// BackgroundShellInfo contains information about a background shell.
type BackgroundShellInfo struct {
	ID          string
	Command     string
	Description string
}

// List returns all background shell IDs.
func (m *BackgroundShellManager) List() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Collect(maps.Keys(m.shells))
}

// KillAll terminates all background shells. The provided context bounds how
// long the function waits for each shell to exit.
func (m *BackgroundShellManager) KillAll(ctx context.Context) {
	m.mu.Lock()
	shells := m.shells
	m.shells = map[string]*BackgroundShell{}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, shell := range shells {
		wg.Go(func() {
			shell.setKillReason("shutdown")
			shell.cancel()
			select {
			case <-shell.done:
			case <-ctx.Done():
			}
		})
	}
	wg.Wait()
}

// GetOutput returns the current output of a background shell.
func (bs *BackgroundShell) GetOutput() (stdout string, stderr string, done bool, err error) {
	select {
	case <-bs.done:
		return bs.stdout.String(), bs.stderr.String(), true, bs.exitErr
	default:
		return bs.stdout.String(), bs.stderr.String(), false, nil
	}
}

// Done returns a channel closed when the job completes.
func (bs *BackgroundShell) Done() <-chan struct{} {
	return bs.done
}

// IsDone checks if the background shell has finished execution.
func (bs *BackgroundShell) IsDone() bool {
	select {
	case <-bs.done:
		return true
	default:
		return false
	}
}

// Wait blocks until the background shell completes.
func (bs *BackgroundShell) Wait() {
	<-bs.done
}

// WaitContext waits for completion until the context is done.
func (bs *BackgroundShell) WaitContext(ctx context.Context) bool {
	select {
	case <-bs.done:
		return true
	case <-ctx.Done():
		return false
	}
}
