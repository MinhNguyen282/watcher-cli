package harness

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"
)

// ProbeRunner executes one probe run and returns its trace. The
// returned error is reserved for infrastructure failures (worktree
// setup, harness persistence); a failed agent run is reported through
// trace.Error instead, so failed probes still participate in
// behavioral contrast.
type ProbeRunner interface {
	RunProbe(ctx context.Context, spec ProbeSpec) (*ExecutionTrace, error)
}

// Optimizer drives the Propose → Probe → Compose loop of the
// self-evolving harness.
type Optimizer struct {
	Store     *Store
	Worktrees WorktreeManager
	Editor    Editor
	Runner    ProbeRunner
	now       func() time.Time
}

// Run executes one task-level recursion: probe the base harness,
// propose K candidates, probe them, compose the winner, distill a
// lesson, persist the refined harness, and apply the winning diff back
// to the main working tree.
func (o *Optimizer) Run(ctx context.Context, task string, opts RunOptions) (*RunResult, error) {
	if err := o.validate(task, &opts); err != nil {
		return nil, err
	}
	logf := opts.Log
	if logf == nil {
		logf = func(format string, args ...any) {
			slog.Info(fmt.Sprintf(format, args...))
		}
	}
	now := o.now
	if now == nil {
		now = time.Now
	}

	result := &RunResult{}
	cleanup := &worktreeCleanup{manager: o.Worktrees, keptBranch: ""}
	defer cleanup.clean(ctx, logf)

	// 1. Base probe: run the task with the current harness in an
	// isolated worktree.
	base, err := o.Store.LoadHarness()
	if err != nil {
		return nil, err
	}
	baseHarness := base
	result.Harness = baseHarness

	logf("Probing base harness %q in an isolated worktree...", baseHarness.ID)
	baseWT, err := o.Worktrees.Add(ctx, "base", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("failed to create base worktree: %w", err)
	}
	cleanup.track(baseWT)

	baseTrace, err := o.runProbe(ctx, ProbeSpec{
		ID:      "base",
		Harness: baseHarness,
		WorkDir: baseWT.Dir,
		Branch:  baseWT.Branch,
		Task:    task,
	}, opts.Timeout, now)
	if err != nil {
		return nil, err
	}
	if err := o.Worktrees.Commit(ctx, baseWT, "crush-harness: probe base"); err != nil {
		logf("Warning: failed to commit base probe: %v", err)
	}
	result.Base = baseTrace
	logf("Base probe finished: tool_calls=%d errors=%d error=%q",
		len(baseTrace.ToolCalls), baseTrace.ComputeStats().ToolErrors, baseTrace.Error)
	if err := o.Store.SaveTrace(baseTrace); err != nil {
		logf("Warning: failed to save base trace: %v", err)
	}

	// 2. Propose K candidate harnesses.
	logf("Proposing %d candidate harnesses...", opts.Probes)
	candidates, err := o.Editor.Propose(ctx, baseHarness, baseTrace, opts.Probes)
	if err != nil {
		return nil, fmt.Errorf("propose step failed: %w", err)
	}
	logf("Proposed %d candidate harnesses.", len(candidates))

	// 3. Probe the candidates.
	outcomes, candidateWTs, err := o.probeCandidates(ctx, task, baseWT, candidates, opts, logf, now)
	if err != nil {
		return nil, err
	}
	result.Candidates = outcomes
	for _, wt := range candidateWTs {
		cleanup.track(wt)
	}

	// 4. Compose: behavioral contrast over all traces.
	logf("Composing results with behavioral contrast...")
	baseOutcome := &ProbeOutcome{Trace: baseTrace, Harness: baseHarness}
	best, refined, rationale, err := o.compose(ctx, baseOutcome, outcomes)
	if err != nil {
		logf("Compose step failed (%v); falling back to heuristic selection.", err)
		best, refined, rationale = heuristicBest(baseOutcome, outcomes)
	}
	result.Best = best
	result.Harness = refined
	logf("Winner: probe %q (harness %q). %s", best.Trace.ProbeID, best.Trace.HarnessID, rationale)

	// 5. Reflect and persist the lesson plus the refined harness.
	lesson, err := o.Editor.Reflect(ctx, baseHarness, refined, baseTrace, best.Trace)
	if err != nil {
		logf("Reflect step failed: %v", err)
	} else {
		result.Lesson = lesson
		if err := o.Store.AppendLesson(lesson); err != nil {
			logf("Warning: failed to save lesson: %v", err)
		} else {
			logf("Lesson saved to .watcher/memory/lessons.md")
		}
	}

	if err := o.Store.SaveHarness(refined); err != nil {
		logf("Warning: failed to persist refined harness: %v", err)
	}

	// 6. Apply the winner's diff to the main working tree.
	if opts.ApplyWinner && best.Trace.ProbeID != baseTrace.ProbeID {
		applied, err := o.Worktrees.ApplyDiff(ctx, best.Trace.GitDiff)
		if err != nil {
			logf("Warning: applying winner diff failed: %v", err)
		}
		result.Applied = applied
		if !applied && best.Trace.GitDiff != "" {
			result.KeptBranch = best.Trace.Branch
			cleanup.keptBranch = best.Trace.Branch
			logf("Winner diff kept on branch %q; merge it with: git merge %s",
				best.Trace.Branch, best.Trace.Branch)
		} else if applied {
			logf("Winner diff applied to the working tree.")
		}
	} else if opts.ApplyWinner {
		result.Applied = true
		logf("Base harness won; nothing to apply.")
	}
	return result, nil
}

// probeCandidates runs every candidate harness in its own worktree.
// Parallel mode starts all probes from HEAD; sequential mode chains
// each candidate onto the previous candidate's branch.
func (o *Optimizer) probeCandidates(
	ctx context.Context,
	task string,
	baseWT *Worktree,
	candidates []*HarnessState,
	opts RunOptions,
	logf func(string, ...any),
	now func() time.Time,
) ([]ProbeOutcome, []*Worktree, error) {
	outcomes := make([]ProbeOutcome, len(candidates))
	worktrees := make([]*Worktree, len(candidates))

	probeOne := func(idx int, baseRef string) (ProbeOutcome, *Worktree, error) {
		cand := candidates[idx]
		id := fmt.Sprintf("cand-%d", idx+1)
		logf("Probing candidate %d/%d (harness %q)...", idx+1, len(candidates), cand.ID)
		wt, err := o.Worktrees.Add(ctx, id, baseRef)
		if err != nil {
			return ProbeOutcome{}, nil, fmt.Errorf("failed to create worktree for %s: %w", id, err)
		}
		trace, err := o.runProbe(ctx, ProbeSpec{
			ID:      id,
			Harness: cand,
			WorkDir: wt.Dir,
			Branch:  wt.Branch,
			Task:    task,
		}, opts.Timeout, now)
		if err != nil {
			return ProbeOutcome{}, wt, err
		}
		if err := o.Worktrees.Commit(ctx, wt, "crush-harness: probe "+id); err != nil {
			logf("Warning: failed to commit probe %s: %v", id, err)
		}
		if err := o.Store.SaveTrace(trace); err != nil {
			logf("Warning: failed to save trace for %s: %v", id, err)
		}
		logf("Candidate %d/%d finished: tool_calls=%d errors=%d error=%q",
			idx+1, len(candidates), len(trace.ToolCalls),
			trace.ComputeStats().ToolErrors, trace.Error)
		return ProbeOutcome{Trace: trace, Harness: cand}, wt, nil
	}

	switch opts.Mode {
	case ModeSequential:
		baseRef := baseWT.Branch
		for i := range candidates {
			oc, wt, err := probeOne(i, baseRef)
			if err != nil {
				return nil, worktrees, err
			}
			outcomes[i] = oc
			worktrees[i] = wt
			baseRef = oc.Trace.Branch
		}
		return outcomes, worktrees, nil

	default: // ModeParallel
		var g errgroup.Group
		g.SetLimit(len(candidates))
		for i := range candidates {
			g.Go(func() error {
				oc, wt, err := probeOne(i, "HEAD")
				if wt != nil {
					worktrees[i] = wt
				}
				if err != nil {
					return err
				}
				outcomes[i] = oc
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return nil, worktrees, err
		}
		return outcomes, worktrees, nil
	}
}

// runProbe runs a single probe with the per-probe timeout applied.
func (o *Optimizer) runProbe(
	ctx context.Context,
	spec ProbeSpec,
	timeout time.Duration,
	now func() time.Time,
) (*ExecutionTrace, error) {
	probeCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		probeCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	trace, err := o.Runner.RunProbe(probeCtx, spec)
	if err != nil {
		return nil, err
	}
	if trace.StartedAt.IsZero() {
		trace.StartedAt = now()
	}
	return trace, nil
}

// compose runs the editor's behavioral-contrast step and resolves the
// winning outcome from the returned probe ID.
func (o *Optimizer) compose(ctx context.Context, base *ProbeOutcome, candidates []ProbeOutcome) (*ProbeOutcome, *HarnessState, string, error) {
	res, err := o.Editor.Compose(ctx, base, candidates)
	if err != nil {
		return nil, nil, "", err
	}
	outcome := findOutcome(res.BestProbeID, base, candidates)
	if outcome == nil {
		return nil, nil, "", fmt.Errorf("compose selected unknown probe %q", res.BestProbeID)
	}
	return outcome, res.Harness, res.Rationale, nil
}

// heuristicBest is the fallback selector used when the compose LLM
// call fails. It prefers candidates that finished without errors and
// produced a diff; ties go to the one with the most verification-like
// tool calls (test runs), then to the base run.
func heuristicBest(base *ProbeOutcome, candidates []ProbeOutcome) (*ProbeOutcome, *HarnessState, string) {
	score := func(oc *ProbeOutcome) int {
		if oc == nil || oc.Trace == nil {
			return -1
		}
		t := oc.Trace
		s := 0
		if t.Error == "" {
			s += 10
		}
		if t.GitDiff != "" {
			s += 5
		}
		for _, tc := range t.ToolCalls {
			switch tc.Name {
			case "bash", "test", "verify":
				s++
			}
		}
		return s
	}

	best := base
	for i := range candidates {
		if score(&candidates[i]) > score(best) {
			best = &candidates[i]
		}
	}
	refined := best.Harness.Sanitize()
	refined.ID = "merged"
	return best, refined, "heuristic selection after compose failure"
}

// findOutcome resolves a probe ID to an outcome.
func findOutcome(id string, base *ProbeOutcome, candidates []ProbeOutcome) *ProbeOutcome {
	if base != nil && base.Trace.ProbeID == id {
		return base
	}
	for i := range candidates {
		if candidates[i].Trace.ProbeID == id {
			return &candidates[i]
		}
	}
	return nil
}

// worktreeCleanup removes every worktree created during a run, except
// the branch explicitly kept for manual merging.
type worktreeCleanup struct {
	manager    WorktreeManager
	tracked    []*Worktree
	keptBranch string
}

func (w *worktreeCleanup) track(wt *Worktree) {
	w.tracked = append(w.tracked, wt)
}

func (w *worktreeCleanup) clean(ctx context.Context, logf func(string, ...any)) {
	for _, wt := range w.tracked {
		keep := wt.Branch == w.keptBranch
		if err := w.manager.Remove(ctx, wt, keep); err != nil {
			logf("Warning: failed to clean up worktree %s: %v", wt.Dir, err)
		}
	}
}

// validate checks the optimizer wiring and normalizes the options.
func (o *Optimizer) validate(task string, opts *RunOptions) error {
	if task == "" {
		return fmt.Errorf("task is empty")
	}
	if o.Store == nil {
		return fmt.Errorf("optimizer store is nil")
	}
	if o.Worktrees == nil {
		return fmt.Errorf("optimizer worktree manager is nil")
	}
	if o.Editor == nil {
		return fmt.Errorf("optimizer editor is nil")
	}
	if o.Runner == nil {
		return fmt.Errorf("optimizer probe runner is nil")
	}
	if opts.Probes <= 0 {
		opts.Probes = 2
	}
	if opts.Mode == "" {
		opts.Mode = ModeParallel
	}
	if !ValidMode(opts.Mode) {
		return fmt.Errorf("invalid probe mode %q (want %q or %q)", opts.Mode, ModeParallel, ModeSequential)
	}
	if opts.Timeout == 0 {
		opts.Timeout = 20 * time.Minute
	}
	if opts.Log == nil {
		opts.Log = func(format string, args ...any) {
			slog.Info(fmt.Sprintf(format, args...))
		}
	}
	return nil
}
