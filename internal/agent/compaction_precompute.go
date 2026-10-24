package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
)

const (
	// precomputeSummaryBudget bounds the background summary call. A summary
	// that has not arrived by then is not worth waiting for on the
	// compaction path, which will regenerate it.
	precomputeSummaryBudget = 3 * time.Minute
	// precomputeSummaryTTL is how long a precomputed summary is trusted.
	precomputeSummaryTTL = 15 * time.Minute
	// idleCompactionDelay is how long a session must sit idle before it
	// compacts itself, and how long its idle check waits.
	idleCompactionDelay = 5 * time.Minute
)

// summaryPrecompute is a summary generated off the turn path. lastMessageID
// anchors it to a position in the transcript so it can be reused only for
// the messages it actually covered.
type summaryPrecompute struct {
	lastMessageID string
	text          string
	usage         fantasy.Usage
	created       time.Time
}

// maybePrecomputeSummary starts a background summary once the session is
// within the precompute band but not yet due for compaction. It is
// single-flight per session: a second call while one runs is a no-op.
func (a *sessionAgent) maybePrecomputeSummary(
	sessionID string,
	model Model,
	promptTokens int64,
	opts fantasy.ProviderOptions,
	onAuthRefresh func(context.Context, *fantasy.ProviderError) error,
) {
	if a.isSubAgent || a.disableAutoSummarize || sessionID == "" {
		return
	}
	budget := newContextBudget(int64(model.CatwalkCfg.ContextWindow), a.contextOptions)
	if budget.window == 0 {
		return
	}
	// At the trigger the compaction path owns the work; precomputing there
	// would race it and bill the user twice.
	if budget.shouldCompact(promptTokens) || !budget.precomputeDue(promptTokens) {
		return
	}

	a.precomputeMu.Lock()
	if a.precomputeInFlight == nil {
		a.precomputeInFlight = make(map[string]bool)
	}
	if a.precomputeInFlight[sessionID] {
		a.precomputeMu.Unlock()
		return
	}
	a.precomputeInFlight[sessionID] = true
	a.precomputeMu.Unlock()

	go func() {
		defer func() {
			a.precomputeMu.Lock()
			delete(a.precomputeInFlight, sessionID)
			a.precomputeMu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), precomputeSummaryBudget)
		defer cancel()

		session, err := a.sessions.Get(ctx, sessionID)
		if err != nil {
			slog.Debug("Skipping summary precompute", "session_id", sessionID, "error", err)
			return
		}
		msgs, err := a.getSessionMessages(ctx, session)
		if err != nil || len(msgs) == 0 {
			return
		}
		aiMsgs, _ := a.preparePrompt(msgs, model.CatwalkCfg.SupportsImages)
		systemPrompt := a.systemPrompt.Get()
		if prefix := a.systemPromptPrefix.Get(); prefix != "" {
			systemPrompt = prefix + "\n\n" + systemPrompt
		}
		if strings.TrimSpace(systemPrompt) == "" {
			systemPrompt = string(summaryPrompt)
		}
		summaryAgent := fantasy.NewAgent(
			model.Model,
			fantasy.WithSystemPrompt(systemPrompt),
			fantasy.WithUserAgent(userAgent),
		)
		resp, err := summaryAgent.Generate(ctx, fantasy.AgentCall{
			Prompt:          string(summaryPrompt) + "\n\n" + buildSummaryPrompt(session.Todos),
			Messages:        aiMsgs,
			Headers:         sessionHeaders(sessionID),
			ProviderOptions: opts,
			OnAuthRefresh:   onAuthRefresh,
		})
		if err != nil {
			slog.Debug("Summary precompute failed", "session_id", sessionID, "error", err)
			return
		}
		if resp.Response.FinishReason == fantasy.FinishReasonLength {
			return
		}
		text := strings.TrimSpace(resp.Response.Content.Text())
		if text == "" {
			return
		}
		a.precomputeMu.Lock()
		if a.precompute == nil {
			a.precompute = make(map[string]*summaryPrecompute)
		}
		a.precompute[sessionID] = &summaryPrecompute{
			lastMessageID: msgs[len(msgs)-1].ID,
			text:          text,
			usage:         resp.TotalUsage,
			created:       time.Now(),
		}
		a.precomputeMu.Unlock()
		slog.Info("Precomputed summary", "session_id", sessionID, "messages", len(msgs))
	}()
}

// takePrecomputedSummary consumes a fresh precompute and splits the
// transcript at the message it covered. The remainder is what still needs
// summarizing, so a compaction pays for the new tail only.
func (a *sessionAgent) takePrecomputedSummary(sessionID string, msgs []message.Message) (string, []message.Message, fantasy.Usage, bool) {
	a.precomputeMu.Lock()
	defer a.precomputeMu.Unlock()
	cached, ok := a.precompute[sessionID]
	if !ok {
		return "", nil, fantasy.Usage{}, false
	}
	delete(a.precompute, sessionID)
	if cached == nil || time.Since(cached.created) > precomputeSummaryTTL {
		return "", nil, fantasy.Usage{}, false
	}
	for i, msg := range msgs {
		if msg.ID == cached.lastMessageID {
			return cached.text, msgs[i+1:], cached.usage, true
		}
	}
	return "", nil, fantasy.Usage{}, false
}

// scheduleIdleCompaction arms a one-shot idle compaction for the session.
// Every new turn replaces the timer, so only a genuinely idle session
// compacts itself.
func (a *sessionAgent) scheduleIdleCompaction(sessionID string) {
	if a.isSubAgent || a.disableAutoSummarize || sessionID == "" {
		return
	}
	if timer, ok := a.idleTimers.Take(sessionID); ok && timer != nil {
		timer.Stop()
	}
	timer := time.AfterFunc(idleCompactionDelay, func() {
		a.compactIdle(sessionID)
	})
	a.idleTimers.Set(sessionID, timer)
}

// compactIdle compacts a session that has been left near the trigger.
func (a *sessionAgent) compactIdle(sessionID string) {
	if a.isSubAgent || a.disableAutoSummarize || a.IsSessionBusy(sessionID) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	session, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return
	}
	budget := newContextBudget(int64(a.models().Large.CatwalkCfg.ContextWindow), a.contextOptions)
	if !budget.idleCompactDue(session.PromptTokens) {
		return
	}
	if err := a.Summarize(ctx, sessionID, a.getCacheControlOptions(), nil); err != nil {
		slog.Info("Idle compaction skipped", "session_id", sessionID, "error", err)
		return
	}
	slog.Info("Idle compaction completed", "session_id", sessionID, "prompt_tokens", session.PromptTokens)
}

// finishSummaryQueue releases the summarize turn and runs one queued prompt.
func (a *sessionAgent) finishSummaryQueue(ctx context.Context, sessionID string) error {
	queuedMessages, ok := a.messageQueue.Get(sessionID)
	if !ok || len(queuedMessages) == 0 {
		return nil
	}
	firstQueuedMessage := queuedMessages[0]
	a.messageQueue.Set(sessionID, queuedMessages[1:])
	_, err := a.Run(ctx, firstQueuedMessage)
	return err
}

// updateSummaryPromptPrefix turns a cached summary into an instruction to
// update it with the newer messages.
func updateSummaryPromptPrefix(cached string) string {
	return fmt.Sprintf(
		"You are updating an earlier summary of this conversation. Here is that summary:\n\n%s\n\nUpdate it with the newer messages below, keeping every decision, file path and user requirement that is still relevant.",
		cached,
	)
}
