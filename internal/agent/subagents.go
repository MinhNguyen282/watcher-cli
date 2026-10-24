package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
)

// SubagentState is the lifecycle state of one background sub-agent.
type SubagentState string

const (
	SubagentRunning   SubagentState = "running"
	SubagentCompleted SubagentState = "completed"
	SubagentFailed    SubagentState = "failed"
	SubagentCancelled SubagentState = "cancelled"
)

const (
	// subagentOutputLimit bounds the text send_message echoes back for a
	// finished run. The child transcript keeps the full output.
	subagentOutputLimit = 12_000
	// maxBackgroundSubagents bounds concurrent background runs. Each one
	// costs a model session, so an unbounded fan-out is a budget leak.
	maxBackgroundSubagents = 8
	// maxTrackedSubagents bounds the run registry. Finished runs stay
	// addressable so the parent can collect them; past this size the
	// oldest finished run is forgotten rather than growing forever.
	maxTrackedSubagents = 64
)

// SubagentInfo is a point-in-time view of a background sub-agent. It is
// safe to serialize; it never carries the live run.
type SubagentInfo struct {
	ID              string        `json:"id"`
	Name            string        `json:"name,omitempty"`
	Type            string        `json:"type"`
	SessionID       string        `json:"session_id"`
	ParentSessionID string        `json:"parent_session_id"`
	State           SubagentState `json:"state"`
	Output          string        `json:"output,omitempty"`
	Error           string        `json:"error,omitempty"`
	StartedAt       time.Time     `json:"started_at"`
	FinishedAt      time.Time     `json:"finished_at,omitzero"`
}

// subagentRun is one addressable background sub-agent. Every turn replaces
// done, so a finished run can be resumed by another message.
type subagentRun struct {
	mu       sync.Mutex
	info     SubagentInfo
	agent    SessionAgent
	ctx      context.Context
	cancel   context.CancelFunc
	turn     func(context.Context, string) (*fantasy.AgentResult, error)
	done     chan struct{}
	finished bool
}

func (r *subagentRun) snapshot() SubagentInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.info
}

// beginTurn marks the run running and returns the channel closed when the
// turn settles.
func (r *subagentRun) beginTurn() chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.info.State = SubagentRunning
	r.info.FinishedAt = time.Time{}
	r.done = make(chan struct{})
	return r.done
}

// settle records the turn outcome exactly once and releases waiters.
func (r *subagentRun) settle(state SubagentState, output, errText string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done == nil {
		// Already settled, or never started: the terminal state is final.
		return
	}
	r.info.State = state
	r.info.Output = output
	r.info.Error = errText
	r.info.FinishedAt = time.Now()
	close(r.done)
	r.done = nil
}

// wait blocks until the current turn settles or ctx ends.
func (r *subagentRun) wait(ctx context.Context) error {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *subagentRun) context() context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ctx
}

// subagentRegistry holds every background run a coordinator started.
type subagentRegistry struct {
	mu    sync.Mutex
	byID  map[string]*subagentRun
	order []string
	seq   uint64
}

func newSubagentRegistry() *subagentRegistry {
	return &subagentRegistry{byID: make(map[string]*subagentRun)}
}

func (r *subagentRegistry) nextID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	return fmt.Sprintf("subagent-%d", r.seq)
}

func (r *subagentRegistry) add(run *subagentRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[run.info.ID] = run
	r.order = append(r.order, run.info.ID)
	if len(r.order) <= maxTrackedSubagents {
		return
	}
	for i, id := range r.order {
		if id == run.info.ID {
			continue
		}
		old := r.byID[id]
		if old != nil && old.snapshot().State == SubagentRunning {
			continue
		}
		delete(r.byID, id)
		r.order = append(r.order[:i], r.order[i+1:]...)
		return
	}
}

// find resolves a run by exact id, unique name, or unique id prefix. Names
// are compared case-insensitively so the model does not have to echo them
// exactly.
func (r *subagentRegistry) find(ref string) (*subagentRun, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if run, ok := r.byID[ref]; ok {
		return run, true
	}
	var byName, byPrefix *subagentRun
	lower := strings.ToLower(ref)
	for _, id := range r.order {
		run := r.byID[id]
		if run == nil {
			continue
		}
		info := run.snapshot()
		if strings.HasPrefix(id, ref) {
			if byPrefix != nil {
				byPrefix = nil
				break
			}
			byPrefix = run
		}
		if info.Name != "" && strings.EqualFold(info.Name, lower) {
			if byName != nil {
				byName = nil
				break
			}
			byName = run
		}
	}
	if byName != nil {
		return byName, true
	}
	if byPrefix != nil {
		return byPrefix, true
	}
	return nil, false
}

// running counts runs with an un-settled turn.
func (r *subagentRegistry) running() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, id := range r.order {
		if run := r.byID[id]; run != nil && run.snapshot().State == SubagentRunning {
			count++
		}
	}
	return count
}

func (r *subagentRegistry) all() []*subagentRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*subagentRun, 0, len(r.order))
	for _, id := range r.order {
		if run := r.byID[id]; run != nil {
			out = append(out, run)
		}
	}
	return out
}

// startSubagent creates the child session and launches the first turn off
// the tool path. It returns a receipt the model can quote in send_message.
func (c *coordinator) startSubagent(ctx context.Context, params subAgentParams) (fantasy.ToolResponse, error) {
	if c.subagents == nil {
		return fantasy.NewTextErrorResponse("background sub-agents are unavailable in this session"), nil
	}
	if c.subagents.running() >= maxBackgroundSubagents {
		return fantasy.NewTextErrorResponse(fmt.Sprintf(
			"refused: %d background sub-agents are already running (limit %d); collect one with send_message before starting another",
			maxBackgroundSubagents, maxBackgroundSubagents,
		)), nil
	}
	session, err := c.createSubagentSession(ctx, params)
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("create session: %w", err)
	}
	if params.InheritContext {
		c.inheritParentContext(ctx, params.SessionID, session.ID)
	}
	turn, err := c.subagentTurn(params.Agent, session.ID)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	run := &subagentRun{
		info: SubagentInfo{
			ID:              c.subagents.nextID(),
			Name:            params.Name,
			Type:            params.Type,
			SessionID:       session.ID,
			ParentSessionID: params.SessionID,
			State:           SubagentRunning,
			StartedAt:       time.Now(),
		},
		agent:  params.Agent,
		ctx:    runCtx,
		cancel: cancel,
		turn:   turn,
	}
	c.subagents.add(run)
	c.emitSubagentHook(ctx, hooks.EventSubagentStart, session.ID, params, nil)

	run.beginTurn()
	go c.runSubagentTurn(run, params, params.Prompt)

	var reply strings.Builder
	fmt.Fprintf(&reply, "Started background sub-agent %s", run.info.ID)
	if run.info.Name != "" {
		fmt.Fprintf(&reply, " (%s)", run.info.Name)
	}
	fmt.Fprintf(&reply, " of type %s in session %s.\n", run.info.Type, session.ID)
	fmt.Fprintf(&reply, "Use send_message with to=%q to wait for its result or to continue it later.", run.info.ID)
	return fantasy.NewTextResponse(reply.String()), nil
}

// runSubagentTurn executes one turn and guarantees the run settles, even if
// the agent panics: a background run that never settles would hang every
// send_message waiting on it.
func (c *coordinator) runSubagentTurn(run *subagentRun, params subAgentParams, prompt string) {
	var (
		result *fantasy.AgentResult
		runErr error
	)
	defer func() {
		if recovered := recover(); recovered != nil {
			runErr = fmt.Errorf("sub-agent panicked: %v", recovered)
		}
		c.settleSubagentRun(run, params, result, runErr)
	}()
	result, runErr = run.turn(run.context(), prompt)
}

// settleSubagentRun records a finished turn and reports it to observers.
func (c *coordinator) settleSubagentRun(run *subagentRun, params subAgentParams, result *fantasy.AgentResult, runErr error) {
	output := subAgentOutput(result)
	state := SubagentCompleted
	errText := ""
	switch {
	case runErr != nil && (errors.Is(runErr, context.Canceled) || errors.Is(run.context().Err(), context.Canceled)):
		state = SubagentCancelled
		errText = "cancelled"
	case runErr != nil:
		state = SubagentFailed
		errText = runErr.Error()
	}
	run.settle(state, output, errText)

	// Cost is best effort; a failed roll-up must not discard the output.
	if err := c.updateParentSessionCost(context.WithoutCancel(run.context()), run.info.SessionID, params.SessionID); err != nil {
		slog.Warn(
			"Failed to update parent session cost",
			"child_session", run.info.SessionID,
			"parent_session", params.SessionID,
			"error", err,
		)
	}

	c.emitSubagentHook(context.WithoutCancel(run.context()), hooks.EventSubagentStop, run.info.SessionID, params, runErr)
	c.announceSubagent(run)
}

// announceSubagent publishes the terminal notification for a background run.
func (c *coordinator) announceSubagent(run *subagentRun) {
	info := run.snapshot()
	message := fmt.Sprintf("Sub-agent %s finished", info.ID)
	if info.Name != "" {
		message = fmt.Sprintf("Sub-agent %q finished", info.Name)
	}
	switch info.State {
	case SubagentFailed:
		message += ": " + info.Error
	case SubagentCancelled:
		message += " (cancelled)"
	}
	if c.notify != nil {
		c.notify.Publish(pubsub.CreatedEvent, notify.Notification{
			SessionID:    info.ParentSessionID,
			Type:         notify.TypeSubagentFinished,
			RunID:        info.ID,
			Message:      message,
			SessionTitle: info.Name,
		})
	}
	c.emitNotificationHook(context.WithoutCancel(run.context()), info.ParentSessionID, "Sub-agent finished", message)
}

// sendSubagentMessage waits for the current turn, optionally continues the
// sub-agent with a new message, and returns its current state.
func (c *coordinator) sendSubagentMessage(ctx context.Context, run *subagentRun, message string) (string, error) {
	message = strings.TrimSpace(message)
	if err := run.wait(ctx); err != nil {
		return describeSubagent(run.snapshot()), nil
	}
	if message == "" {
		return describeSubagent(run.snapshot()), nil
	}

	info := run.snapshot()
	done := run.beginTurn()
	go c.runSubagentTurn(run, subAgentParams{
		Agent:     run.agent,
		SessionID: info.ParentSessionID,
	}, message)

	select {
	case <-done:
	case <-ctx.Done():
	}
	return describeSubagent(run.snapshot()), nil
}

func describeSubagent(info SubagentInfo) string {
	var out strings.Builder
	fmt.Fprintf(&out, "sub-agent %s", info.ID)
	if info.Name != "" {
		fmt.Fprintf(&out, " (%s)", info.Name)
	}
	fmt.Fprintf(&out, "\ntype: %s\nstate: %s\nsession: %s\n", info.Type, info.State, info.SessionID)
	if info.Error != "" {
		fmt.Fprintf(&out, "error: %s\n", info.Error)
	}
	output := info.Output
	if len(output) > subagentOutputLimit {
		cut := subagentOutputLimit
		for cut > 0 && !utf8.RuneStart(output[cut]) {
			cut--
		}
		output = output[:cut] + "\n[output truncated; read the child session for the rest]"
	}
	if output != "" {
		fmt.Fprintf(&out, "output:\n%s\n", output)
	}
	return out.String()
}

func (c *coordinator) sendMessageTool(ctx context.Context) (fantasy.AgentTool, error) {
	return fantasy.NewAgentTool(
		sendMessageToolName,
		sendMessageDescription,
		func(ctx context.Context, params SendMessageParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if c.subagents == nil {
				return fantasy.NewTextErrorResponse("background sub-agents are unavailable in this session"), nil
			}
			run, ok := c.subagents.find(params.To)
			if !ok {
				return fantasy.NewTextErrorResponse("no background sub-agent matches " + params.To + "; use the id or name returned by the agent tool"), nil
			}
			out, err := c.sendSubagentMessage(ctx, run, params.Message)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			return fantasy.NewTextResponse(out), nil
		},
	), nil
}

// cancelSubagentsForSession cancels background runs whose parent session is
// the one being cancelled. Their terminal notifications still fire so the
// caller sees the cancellation.
func (c *coordinator) cancelSubagentsForSession(sessionID string) {
	if c.subagents == nil {
		return
	}
	for _, run := range c.subagents.all() {
		if run.snapshot().ParentSessionID != sessionID {
			continue
		}
		run.mu.Lock()
		cancel := run.cancel
		run.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
}

func (c *coordinator) cancelAllSubagents() {
	if c.subagents == nil {
		return
	}
	for _, run := range c.subagents.all() {
		run.mu.Lock()
		cancel := run.cancel
		run.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
}

// createSubagentSession derives the child session for a delegation.
func (c *coordinator) createSubagentSession(ctx context.Context, params subAgentParams) (session.Session, error) {
	agentToolSessionID := c.sessions.CreateAgentToolSessionID(params.AgentMessageID, params.ToolCallID)
	return c.sessions.CreateTaskSession(ctx, agentToolSessionID, params.SessionID, params.SessionTitle)
}

// subagentTurn builds the turn runner for a child session. The model and
// provider options are resolved once so repeated turns against the same
// child stay cheap.
func (c *coordinator) subagentTurn(agent SessionAgent, sessionID string) (func(context.Context, string) (*fantasy.AgentResult, error), error) {
	model := agent.Model()
	maxTokens := model.CatwalkCfg.DefaultMaxTokens
	if model.ModelCfg.MaxTokens != 0 {
		maxTokens = model.ModelCfg.MaxTokens
	}
	providerCfg, ok := c.cfg.Config().Providers.Get(model.ModelCfg.Provider)
	if !ok {
		return nil, errModelProviderNotConfigured
	}
	return func(ctx context.Context, prompt string) (*fantasy.AgentResult, error) {
		opts := getProviderOptions(model, providerCfg)
		injectOpenrouterSessionID(opts, sessionID)
		result, err := agent.Run(ctx, SessionAgentCall{
			SessionID:        sessionID,
			Prompt:           prompt,
			MaxOutputTokens:  maxTokens,
			ProviderOptions:  opts,
			Temperature:      model.ModelCfg.Temperature,
			TopP:             model.ModelCfg.TopP,
			TopK:             callTopK(providerCfg, model.ModelCfg.TopK),
			FrequencyPenalty: model.ModelCfg.FrequencyPenalty,
			PresencePenalty:  model.ModelCfg.PresencePenalty,
			NonInteractive:   true,
			OnAuthRefresh:    c.makeAuthRefreshCallback(providerCfg),
		})
		c.askToSignInAgain(err, model)
		return result, err
	}, nil
}
