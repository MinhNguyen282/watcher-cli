package permission

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
)

// hookApprovalKey is the unexported context key used to mark a tool call as
// pre-approved by a PreToolUse hook. The value is the tool call ID so an
// approval can't be reused across calls that happen to share a context.
type hookApprovalKey struct{}

// WithHookApproval returns a context that marks the given tool call ID as
// pre-approved by a hook. When the permission service sees a matching
// request it short-circuits the normal prompt and grants immediately.
func WithHookApproval(ctx context.Context, toolCallID string) context.Context {
	return context.WithValue(ctx, hookApprovalKey{}, toolCallID)
}

// hookApproved reports whether the context carries a hook approval for the
// given tool call ID.
func hookApproved(ctx context.Context, toolCallID string) bool {
	if toolCallID == "" {
		return false
	}
	v, _ := ctx.Value(hookApprovalKey{}).(string)
	return v == toolCallID
}

type CreatePermissionRequest struct {
	SessionID   string `json:"session_id"`
	ToolCallID  string `json:"tool_call_id"`
	ToolName    string `json:"tool_name"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Params      any    `json:"params"`
	Path        string `json:"path"`
	// RuleContent is the call's content for rule matching: the file path,
	// shell command, URL or query selected by the tool spec. The dispatch
	// pipeline fills it so every tool gets content-scoped rules without
	// repeating the extraction.
	RuleContent string `json:"rule_content,omitempty"`
	// Mutating reports whether the tool can change state. Plan mode uses it
	// to refuse the call before any prompt is shown.
	Mutating bool `json:"mutating,omitempty"`
	// ReadOnly reports whether the tool is classified read-only.
	ReadOnly bool `json:"read_only,omitempty"`
}

type PermissionNotification struct {
	SessionID  string `json:"session_id"`
	ToolCallID string `json:"tool_call_id"`
	Granted    bool   `json:"granted"`
	Denied     bool   `json:"denied"`
	// Stage and Reason describe which stage decided, when a decision has
	// already been made. They stay empty for the initial "requested" event.
	Stage  Stage  `json:"stage,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type PermissionRequest struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	ToolCallID  string `json:"tool_call_id"`
	ToolName    string `json:"tool_name"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Params      any    `json:"params"`
	Path        string `json:"path"`
	// RuleContent is what a persisted rule would be scoped to.
	RuleContent string `json:"rule_content,omitempty"`
}

type Service interface {
	pubsub.Subscriber[PermissionRequest]
	// GrantPersistent grants a permission request and remembers the grant
	// for the session. It returns true if this call actually resolved the
	// pending request; false if the request had already been resolved
	// (e.g., by another concurrent caller) or is unknown.
	GrantPersistent(permission PermissionRequest) bool
	// Grant grants a permission request. It returns true if this call
	// actually resolved the pending request; false if the request had
	// already been resolved or is unknown.
	Grant(permission PermissionRequest) bool
	// Deny denies a permission request. It returns true if this call
	// actually resolved the pending request; false if the request had
	// already been resolved or is unknown.
	Deny(permission PermissionRequest) bool
	Request(ctx context.Context, opts CreatePermissionRequest) (bool, error)
	// ActiveRequest returns the request that is currently awaiting a
	// decision, or false when no request is pending. A request is only
	// reported while it can still be resolved; a request whose waiter
	// was cancelled is not returned.
	ActiveRequest() (PermissionRequest, bool)
	AutoApproveSession(sessionID string)
	SetSkipRequests(skip bool)
	SkipRequests() bool
	SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[PermissionNotification]
}

// PermissionKey is a composite key for session permission lookups.
type PermissionKey struct {
	SessionID string
	ToolName  string
	Action    string
	Path      string
}

type permissionService struct {
	*pubsub.Broker[PermissionRequest]

	notificationBroker    *pubsub.Broker[PermissionNotification]
	workingDir            string
	sessionPermissions    *csync.Map[PermissionKey, bool]
	pendingRequests       *csync.Map[string, chan bool]
	autoApproveSessions   map[string]bool
	autoApproveSessionsMu sync.RWMutex
	skip                  atomic.Bool
	allowedTools          []string

	mode        Mode
	modeOptions ModeOptions
	rules       []Rule
	policyMu    sync.RWMutex
	decisions   *csync.Map[string, Decision]

	// used to make sure we only process one request at a time
	requestMu       sync.Mutex
	activeRequest   *PermissionRequest
	activeRequestMu sync.Mutex
}

// resolve atomically removes the pending request entry for the given
// permission and, if it was still pending, publishes exactly one
// PermissionNotification and forwards the outcome to the waiter on
// respCh. It returns true if this call resolved the request, false if
// it had already been resolved (e.g., by another concurrent caller) or
// the request ID is unknown.
//
// If onResolve is non-nil it runs after the pending entry has been
// taken but before the notification is published or the waiter is
// unblocked. This lets GrantPersistent record the session permission
// only when it actually wins the race, so a losing GrantPersistent
// that lost to a Deny does not leak an auto-approve entry.
//
// All three public resolution methods (Grant, GrantPersistent, Deny)
// route through this helper so multi-subscriber UIs can race safely:
// the first caller wins, the rest become no-ops.
func (s *permissionService) resolve(permission PermissionRequest, granted, denied bool, onResolve func()) bool {
	respCh, ok := s.pendingRequests.Take(permission.ID)
	if !ok {
		return false
	}

	if onResolve != nil {
		onResolve()
	}

	s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
		SessionID:  permission.SessionID,
		ToolCallID: permission.ToolCallID,
		Granted:    granted,
		Denied:     denied,
		Stage:      StageUser,
	})

	// respCh is buffered (cap 1) and only ever has at most one sender
	// per request because Take removes the entry under the map lock,
	// so this send never blocks.
	respCh <- granted

	s.activeRequestMu.Lock()
	if s.activeRequest != nil && s.activeRequest.ID == permission.ID {
		s.activeRequest = nil
	}
	s.activeRequestMu.Unlock()
	return true
}

func (s *permissionService) GrantPersistent(permission PermissionRequest) bool {
	// Record the persistent grant only if this call wins the
	// pending-request race. Otherwise a losing GrantPersistent that
	// lost to a Deny would still leave an auto-approve entry behind,
	// silently flipping later denied calls to allowed.
	return s.resolve(permission, true, false, func() {
		s.sessionPermissions.Set(PermissionKey{
			SessionID: permission.SessionID,
			ToolName:  permission.ToolName,
			Action:    permission.Action,
			Path:      permission.Path,
		}, true)
	})
}

func (s *permissionService) Grant(permission PermissionRequest) bool {
	return s.resolve(permission, true, false, nil)
}

func (s *permissionService) Deny(permission PermissionRequest) bool {
	return s.resolve(permission, false, true, nil)
}

func (s *permissionService) Request(ctx context.Context, opts CreatePermissionRequest) (bool, error) {
	decision := s.Decide(ctx, opts)
	if opts.ToolCallID != "" {
		s.decisions.Set(opts.ToolCallID, decision)
	}
	if decision.Allowed() {
		s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
			SessionID:  opts.SessionID,
			ToolCallID: opts.ToolCallID,
			Granted:    true,
			Stage:      decision.Stage,
			Reason:     decision.Reason,
		})
		return true, nil
	}
	if decision.Denied() {
		s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
			SessionID:  opts.SessionID,
			ToolCallID: opts.ToolCallID,
			Denied:     true,
			Stage:      decision.Stage,
			Reason:     decision.Reason,
		})
		return false, nil
	}

	s.requestMu.Lock()
	defer s.requestMu.Unlock()

	// Tell the UI that a permission was requested.
	s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
		SessionID:  opts.SessionID,
		ToolCallID: opts.ToolCallID,
	})

	s.autoApproveSessionsMu.RLock()
	autoApprove := s.autoApproveSessions[opts.SessionID]
	s.autoApproveSessionsMu.RUnlock()

	if autoApprove {
		s.notificationBroker.Publish(pubsub.CreatedEvent, PermissionNotification{
			SessionID:  opts.SessionID,
			ToolCallID: opts.ToolCallID,
			Granted:    true,
		})
		return true, nil
	}

	permission := PermissionRequest{
		ID:          uuid.New().String(),
		Path:        s.permissionPath(opts),
		SessionID:   opts.SessionID,
		ToolCallID:  opts.ToolCallID,
		ToolName:    opts.ToolName,
		Description: opts.Description,
		Action:      opts.Action,
		Params:      opts.Params,
		RuleContent: opts.RuleContent,
	}

	s.activeRequestMu.Lock()
	s.activeRequest = &permission
	s.activeRequestMu.Unlock()

	respCh := make(chan bool, 1)
	s.pendingRequests.Set(permission.ID, respCh)
	defer s.pendingRequests.Del(permission.ID)

	// Publish the request.
	s.Publish(pubsub.CreatedEvent, permission)

	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case granted := <-respCh:
		return granted, nil
	}
}

// Decide runs the non-interactive half of a permission check: modes, rules,
// the allowlist, hook approvals, session grants and bypass. It never blocks.
// A Prompt decision means the caller must ask the user.
func (s *permissionService) Decide(ctx context.Context, opts CreatePermissionRequest) Decision {
	if s.skip.Load() {
		return Decision{Granted: true, Stage: StageSkip, Reason: "permission prompts are disabled"}
	}

	mode := s.Mode()
	rules := s.Rules()
	tool, content := opts.ToolName, s.ruleContent(opts)

	if rule, ok := EvaluateRules(rules, tool, content, BehaviorDeny); ok {
		return Decision{Stage: StageRuleDeny, Reason: "denied by rule: " + rule.String(), Rule: rule}
	}
	if rule, ok := EvaluateRules(rules, tool, content, BehaviorAsk); ok {
		return Decision{Prompt: true, Stage: StageRuleAsk, Reason: "rule requires confirmation: " + rule.String(), Rule: rule}
	}
	if rule, ok := EvaluateRules(rules, tool, content, BehaviorAllow); ok {
		return Decision{Granted: true, Stage: StageRuleAllow, Reason: "allowed by rule: " + rule.String(), Rule: rule}
	}

	commandKey := tool + ":" + opts.Action
	if slices.Contains(s.allowedTools, commandKey) || slices.Contains(s.allowedTools, tool) {
		return Decision{Granted: true, Stage: StageAllowlist, Reason: "tool is in the allowed tools list"}
	}

	if hookApproved(ctx, opts.ToolCallID) {
		return Decision{Granted: true, Stage: StageHook, Reason: "approved by PreToolUse hook"}
	}

	if mode == ModePlan && opts.Mutating {
		return Decision{Stage: StageModePlan, Reason: "plan mode is read-only"}
	}

	if mode == ModeBypassPermissions {
		return Decision{Granted: true, Stage: StageModeBypass, Reason: "bypass permissions mode"}
	}

	if mode.CanAutoApprove(true) {
		if !opts.Mutating {
			return Decision{Granted: true, Stage: StageModeAuto, Reason: "read-only tool in " + string(mode) + " mode"}
		}
		if tool == "bash" && IsReadOnlyCommand(content) {
			return Decision{Granted: true, Stage: StageAllowlist, Reason: "read-only command in " + string(mode) + " mode"}
		}
	}

	if _, ok := s.sessionPermissions.Get(PermissionKey{
		SessionID: opts.SessionID,
		ToolName:  opts.ToolName,
		Action:    opts.Action,
		Path:      s.permissionPath(opts),
	}); ok {
		return Decision{Granted: true, Stage: StageSession, Reason: "approved earlier in this session"}
	}

	if mode == ModeDontAsk {
		return Decision{Stage: StageModeDontAsk, Reason: "dontAsk mode denies anything that would prompt"}
	}

	return Decision{Prompt: true, Stage: StageUser}
}

// ruleContent picks the content a rule is matched against, falling back to
// the action and path for callers that do not extract a rule field.
func (s *permissionService) ruleContent(opts CreatePermissionRequest) string {
	if opts.RuleContent != "" {
		return opts.RuleContent
	}
	if opts.ToolName == "bash" {
		return opts.Action
	}
	if opts.Path != "" {
		return opts.Path
	}
	return opts.Action
}

// permissionPath normalizes the directory a session grant is scoped to.
func (s *permissionService) permissionPath(opts CreatePermissionRequest) string {
	dir := opts.Path
	if fileInfo, err := os.Stat(opts.Path); err == nil && !fileInfo.IsDir() {
		dir = filepath.Dir(opts.Path)
	}
	if dir == "." || dir == "" {
		dir = s.workingDir
	}
	return dir
}

func (s *permissionService) SetMode(mode Mode) {
	s.policyMu.Lock()
	s.mode = mode
	s.policyMu.Unlock()
}

func (s *permissionService) Mode() Mode {
	s.policyMu.RLock()
	defer s.policyMu.RUnlock()
	if s.mode == "" {
		return ModeDefault
	}
	return s.mode
}

func (s *permissionService) SetModeOptions(options ModeOptions) {
	s.policyMu.Lock()
	s.modeOptions = options
	s.policyMu.Unlock()
}

func (s *permissionService) ModeOptions() ModeOptions {
	s.policyMu.RLock()
	defer s.policyMu.RUnlock()
	return s.modeOptions
}

func (s *permissionService) SetRules(rules []Rule) {
	copied := make([]Rule, len(rules))
	copy(copied, rules)
	s.policyMu.Lock()
	s.rules = copied
	s.policyMu.Unlock()
}

func (s *permissionService) Rules() []Rule {
	s.policyMu.RLock()
	defer s.policyMu.RUnlock()
	copied := make([]Rule, len(s.rules))
	copy(copied, s.rules)
	return copied
}

func (s *permissionService) DecisionFor(toolCallID string) (Decision, bool) {
	if toolCallID == "" {
		return Decision{}, false
	}
	return s.decisions.Get(toolCallID)
}

// ActiveRequest returns the request that is currently awaiting a
// decision. The active pointer can outlive the waiter that published
// it (Request leaves it behind when its context is cancelled), so a
// request is only reported while its ID is still registered in
// pendingRequests, i.e. while Grant/Deny can actually resolve it.
func (s *permissionService) ActiveRequest() (PermissionRequest, bool) {
	s.activeRequestMu.Lock()
	active := s.activeRequest
	s.activeRequestMu.Unlock()
	if active == nil {
		return PermissionRequest{}, false
	}
	if _, ok := s.pendingRequests.Get(active.ID); !ok {
		return PermissionRequest{}, false
	}
	return *active, true
}

func (s *permissionService) AutoApproveSession(sessionID string) {
	s.autoApproveSessionsMu.Lock()
	s.autoApproveSessions[sessionID] = true
	s.autoApproveSessionsMu.Unlock()
}

func (s *permissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[PermissionNotification] {
	return s.notificationBroker.Subscribe(ctx)
}

func (s *permissionService) SetSkipRequests(skip bool) {
	s.skip.Store(skip)
}

func (s *permissionService) SkipRequests() bool {
	return s.skip.Load()
}

func NewPermissionService(workingDir string, skip bool, allowedTools []string) Service {
	svc := &permissionService{
		Broker:              pubsub.NewBroker[PermissionRequest](),
		notificationBroker:  pubsub.NewBroker[PermissionNotification](),
		workingDir:          workingDir,
		sessionPermissions:  csync.NewMap[PermissionKey, bool](),
		autoApproveSessions: make(map[string]bool),
		allowedTools:        allowedTools,
		pendingRequests:     csync.NewMap[string, chan bool](),
		decisions:           csync.NewMap[string, Decision](),
		modeOptions:         ModeOptions{BypassAllowed: true},
	}
	svc.skip.Store(skip)
	return svc
}
