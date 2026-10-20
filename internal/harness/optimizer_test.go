package harness

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeWorktrees implements WorktreeManager without touching git.
type fakeWorktrees struct {
	added      []*Worktree
	addRefs    []string
	removed    []string
	keptBranch string
	applyErr   error
	applied    bool
}

func (f *fakeWorktrees) Add(_ context.Context, id, baseRef string) (*Worktree, error) {
	wt := &Worktree{
		ID:     id,
		Dir:    "/tmp/" + id,
		Branch: "crush-harness-" + id,
	}
	f.added = append(f.added, wt)
	f.addRefs = append(f.addRefs, baseRef)
	return wt, nil
}

func (f *fakeWorktrees) Remove(_ context.Context, wt *Worktree, keepBranch bool) error {
	if wt == nil {
		return nil
	}
	if keepBranch {
		f.keptBranch = wt.Branch
	} else {
		f.removed = append(f.removed, wt.Branch)
	}
	return nil
}

func (f *fakeWorktrees) Commit(_ context.Context, wt *Worktree, message string) error {
	return nil
}

func (f *fakeWorktrees) Diff(_ context.Context, _ *Worktree) (string, error) {
	return "", nil
}

func (f *fakeWorktrees) ApplyDiff(_ context.Context, diff string) (bool, error) {
	if f.applyErr != nil {
		return false, f.applyErr
	}
	f.applied = true
	return true, nil
}

// fakeRunner records probe specs and returns scripted traces.
type fakeRunner struct {
	specs []ProbeSpec
}

func (f *fakeRunner) RunProbe(_ context.Context, spec ProbeSpec) (*ExecutionTrace, error) {
	f.specs = append(f.specs, spec)
	diff := "diff for " + spec.ID
	if spec.ID == "base" {
		// The base run models the current behavior: no code change.
		diff = ""
	}
	return &ExecutionTrace{
		ProbeID:    spec.ID,
		HarnessID:  spec.Harness.ID,
		TaskPrompt: spec.Task,
		WorkingDir: spec.WorkDir,
		Branch:     spec.Branch,
		ToolCalls:  []ToolCallRecord{{Name: "bash", Input: "go test ./..."}},
		GitDiff:    diff,
		Duration:   time.Second,
	}, nil
}

// fakeEditor returns fixed candidates and picks the first candidate
// as winner.
type fakeEditor struct {
	proposed int
}

func (f *fakeEditor) Propose(_ context.Context, _ *HarnessState, _ *ExecutionTrace, k int) ([]*HarnessState, error) {
	f.proposed = k
	candidates := make([]*HarnessState, 0, k)
	for i := 0; i < k; i++ {
		candidates = append(candidates, &HarnessState{ID: fmt.Sprintf("cand-%d", i+1)})
	}
	return candidates, nil
}

func (f *fakeEditor) Compose(_ context.Context, _ *ProbeOutcome, candidates []ProbeOutcome) (*ComposeResult, error) {
	winner := candidates[0]
	return &ComposeResult{
		BestProbeID: winner.Trace.ProbeID,
		Rationale:   "the first candidate behaved best",
		Harness: &HarnessState{
			ID:     "merged",
			Memory: []string{"run focused tests"},
		},
	}, nil
}

func (f *fakeEditor) Reflect(_ context.Context, _, _ *HarnessState, _, _ *ExecutionTrace) (string, error) {
	return "Run focused tests instead of the whole suite.", nil
}

func newTestOptimizer(dir string) (*Optimizer, *fakeWorktrees, *fakeRunner, *fakeEditor) {
	store := NewStore(dir)
	worktrees := &fakeWorktrees{}
	runner := &fakeRunner{}
	editor := &fakeEditor{}
	return &Optimizer{
		Store:     store,
		Worktrees: worktrees,
		Editor:    editor,
		Runner:    runner,
		now:       func() time.Time { return time.Unix(1_700_000_000, 0) },
	}, worktrees, runner, editor
}

func TestOptimizerRunParallel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	opt, wtm, runner, editor := newTestOptimizer(dir)

	opts := DefaultRunOptions()
	opts.Probes = 2
	opts.Mode = ModeParallel
	opts.Timeout = time.Minute
	opts.ApplyWinner = true

	result, err := opt.Run(context.Background(), "Fix the race", opts)
	require.NoError(t, err)

	// Base probe plus two candidates.
	require.Len(t, runner.specs, 3)
	require.Equal(t, "base", runner.specs[0].ID)
	require.Equal(t, "cand-1", runner.specs[1].ID)
	require.Equal(t, "cand-2", runner.specs[2].ID)
	require.Equal(t, "base", runner.specs[0].Harness.ID)
	require.Equal(t, 2, editor.proposed)

	// Winner selection and application.
	require.Equal(t, "cand-1", result.Best.Trace.ProbeID)
	require.True(t, wtm.applied)
	require.True(t, result.Applied)
	require.Equal(t, "Run focused tests instead of the whole suite.", result.Lesson)

	// The refined harness is persisted for the next run.
	saved, err := opt.Store.LoadHarness()
	require.NoError(t, err)
	require.Equal(t, "merged", saved.ID)
	require.Equal(t, []string{"run focused tests"}, saved.Memory)
	require.Contains(t, saved.RenderedSystemPrompt, "run focused tests")

	// All worktrees were cleaned up.
	require.Len(t, wtm.removed, 3)
}

func TestOptimizerRunSequentialChainsBranches(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	opt, wtm, runner, _ := newTestOptimizer(dir)

	opts := DefaultRunOptions()
	opts.Probes = 2
	opts.Mode = ModeSequential
	opts.Timeout = time.Minute

	_, err := opt.Run(context.Background(), "Refactor auth", opts)
	require.NoError(t, err)

	require.Len(t, runner.specs, 3)
	// Add is called: base from HEAD, cand-1 from the base branch,
	// cand-2 from the cand-1 branch.
	require.Equal(t, "HEAD", wtm.addRefs[0])
	require.Equal(t, runner.specs[0].Branch, wtm.addRefs[1])
	require.Equal(t, runner.specs[1].Branch, wtm.addRefs[2])
}

func TestOptimizerRunKeepsBranchWhenApplyFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	opt, wtm, _, _ := newTestOptimizer(dir)
	wtm.applyErr = fmt.Errorf("dirty tree")

	opts := DefaultRunOptions()
	opts.Probes = 1
	opts.ApplyWinner = true

	result, err := opt.Run(context.Background(), "task", opts)
	require.NoError(t, err)
	require.False(t, result.Applied)
	require.NotEmpty(t, result.KeptBranch)
	require.Equal(t, result.KeptBranch, wtm.keptBranch)
}

// proposeOnlyEditor proposes fine but fails to compose, exercising the
// heuristic fallback path.
type proposeOnlyEditor struct {
	proposed int
}

func (e *proposeOnlyEditor) Propose(_ context.Context, _ *HarnessState, _ *ExecutionTrace, k int) ([]*HarnessState, error) {
	e.proposed = k
	candidates := make([]*HarnessState, 0, k)
	for i := 0; i < k; i++ {
		candidates = append(candidates, &HarnessState{ID: fmt.Sprintf("cand-%d", i+1)})
	}
	return candidates, nil
}

func (e *proposeOnlyEditor) Compose(_ context.Context, _ *ProbeOutcome, _ []ProbeOutcome) (*ComposeResult, error) {
	return nil, fmt.Errorf("compose LLM down")
}

func (e *proposeOnlyEditor) Reflect(_ context.Context, _, _ *HarnessState, _, _ *ExecutionTrace) (string, error) {
	return "Keep verifying.", nil
}

func TestOptimizerRunComposeFallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	editor := &proposeOnlyEditor{}
	runner := &fakeRunner{}
	opt := &Optimizer{
		Store:     NewStore(dir),
		Worktrees: &fakeWorktrees{},
		Editor:    editor,
		Runner:    runner,
		now:       func() time.Time { return time.Unix(1_700_000_000, 0) },
	}

	opts := DefaultRunOptions()
	opts.Probes = 1
	opts.Timeout = time.Minute

	result, err := opt.Run(context.Background(), "task", opts)
	require.NoError(t, err)
	// The heuristic prefers the candidate (no error, has a diff) over
	// the base run.
	require.Equal(t, "cand-1", result.Best.Trace.ProbeID)
	require.Equal(t, "merged", result.Harness.ID)
	require.Equal(t, "Keep verifying.", result.Lesson)
}

func TestOptimizerValidate(t *testing.T) {
	t.Parallel()
	opt := &Optimizer{}
	require.Error(t, opt.Run(context.Background(), "task", RunOptions{}))

	_, wtm, runner, editor := newTestOptimizer(t.TempDir())
	opt = &Optimizer{Store: NewStore(t.TempDir()), Worktrees: wtm, Editor: editor, Runner: runner}
	require.Error(t, opt.Run(context.Background(), "", RunOptions{}))
}
