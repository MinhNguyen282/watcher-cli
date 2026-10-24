package permission

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMode(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"default", "plan", "acceptEdits", "auto", "bypassPermissions", "dontAsk"} {
		mode, err := ParseMode(value)
		require.NoError(t, err, value)
		require.Equal(t, Mode(value), mode)
	}

	empty, err := ParseMode("  ")
	require.NoError(t, err)
	require.Equal(t, ModeDefault, empty)

	_, err = ParseMode("yolo")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown permission mode")
}

func TestValidateTransition(t *testing.T) {
	t.Parallel()

	require.NoError(t, ValidateTransition(ModeDefault, ModeAcceptEdits, ModeOptions{BypassAllowed: true}))

	err := ValidateTransition(ModeDefault, ModeBypassPermissions, ModeOptions{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "disabled")

	err = ValidateTransition(ModePlan, ModeDefault, ModeOptions{BypassAllowed: true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "explicit approval")

	require.NoError(t, ValidateTransition(ModePlan, ModeDefault, ModeOptions{BypassAllowed: true, PlanExitApproved: true}))
}

func TestModeRank(t *testing.T) {
	t.Parallel()

	require.True(t, ModeBypassPermissions.StrongerThan(ModeAuto))
	require.True(t, ModeAuto.StrongerThan(ModeDefault))
	require.False(t, ModePlan.StrongerThan(ModeDefault))
	require.True(t, ModeDefault.StrongerThan(ModeDontAsk))
}

func TestParseRule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		spec    string
		tool    string
		content string
	}{
		{"bash", "bash", ""},
		{"Bash(git status)", "bash", "git status"},
		{"edit(./internal/**)", "edit", "./internal/**"},
		{"web_fetch(domain:example.com)", "web_fetch", "domain:example.com"},
	}

	for _, tt := range tests {
		rule, err := ParseRule(tt.spec, BehaviorAllow, DestinationProjectSettings)
		require.NoError(t, err, tt.spec)
		require.Equal(t, tt.tool, rule.Tool)
		require.Equal(t, tt.content, rule.Content)
		require.Equal(t, BehaviorAllow, rule.Behavior)
	}

	for _, spec := range []string{"", "bash(git status", "  (x)"} {
		_, err := ParseRule(spec, BehaviorAllow, DestinationSession)
		require.Error(t, err, spec)
	}
}

func TestRuleMatches(t *testing.T) {
	t.Parallel()

	t.Run("command exact and prefix", func(t *testing.T) {
		exact := Rule{Tool: "bash", Content: "git status", Behavior: BehaviorAllow}
		require.True(t, exact.Matches("bash", "git status"))
		require.False(t, exact.Matches("bash", "git status --short"))

		prefix := Rule{Tool: "bash", Content: "git status:*", Behavior: BehaviorAllow}
		require.True(t, prefix.Matches("bash", "git status"))
		require.True(t, prefix.Matches("bash", "git status --short"))
		require.False(t, prefix.Matches("bash", "git push"))
	})

	t.Run("paths", func(t *testing.T) {
		dir := Rule{Tool: "edit", Content: "internal/agent", Behavior: BehaviorAllow}
		require.True(t, dir.Matches("edit", "internal/agent/agent.go"))
		require.False(t, dir.Matches("edit", "internal/agents/x.go"))

		glob := Rule{Tool: "edit", Content: "internal/**/*.go", Behavior: BehaviorAllow}
		require.True(t, glob.Matches("edit", "internal/agent/agent.go"))
		require.False(t, glob.Matches("edit", "docs/plan.md"))
	})

	t.Run("domains", func(t *testing.T) {
		domain := Rule{Tool: "web_fetch", Content: "domain:example.com", Behavior: BehaviorAllow}
		require.True(t, domain.Matches("web_fetch", "https://api.example.com/v1"))
		require.True(t, domain.Matches("web_fetch", "example.com"))
		require.False(t, domain.Matches("web_fetch", "https://notexample.com/x"))

		url := Rule{Tool: "web_fetch", Content: "https://example.com/docs", Behavior: BehaviorAllow}
		require.True(t, url.Matches("web_fetch", "https://example.com/docs/page"))
		require.False(t, url.Matches("web_fetch", "https://example.com/other"))
	})

	t.Run("tool-wide", func(t *testing.T) {
		rule := Rule{Tool: "crush_logs", Behavior: BehaviorAllow}
		require.True(t, rule.Matches("crush_logs", ""))
		require.False(t, rule.Matches("bash", ""))
	})
}

func TestIsReadOnlyCommand(t *testing.T) {
	t.Parallel()

	for _, command := range []string{
		"ls -la",
		"git status",
		"git status --short",
		"git diff HEAD",
		"git log --oneline -5",
		"rg -n proto",
		"cat go.mod",
		"pwd",
		"wc -l main.go",
	} {
		require.True(t, IsReadOnlyCommand(command), command)
	}

	for _, command := range []string{
		"",
		"rm -rf /tmp/x",
		"git push origin main",
		"git branch -D main",
		"git config user.name x",
		"git -C /tmp status",
		"ls | grep x",
		"cat file > out",
		"echo hi && rm x",
		"$(whoami)",
		"./cat go.mod",
		"node -e 1",
	} {
		require.False(t, IsReadOnlyCommand(command), command)
	}
}

func TestDecidePrecedence(t *testing.T) {
	t.Parallel()

	newService := func(t *testing.T) *permissionService {
		t.Helper()
		return NewPermissionService(t.TempDir(), false, nil).(*permissionService)
	}

	req := func(tool, content string) CreatePermissionRequest {
		return CreatePermissionRequest{
			SessionID:   "s1",
			ToolCallID:  "c1",
			ToolName:    tool,
			Action:      "run",
			RuleContent: content,
			Mutating:    true,
		}
	}

	t.Run("skip allows everything", func(t *testing.T) {
		svc := newService(t)
		svc.SetSkipRequests(true)
		require.Equal(t, StageSkip, svc.Decide(context.Background(), req("bash", "rm -rf /")).Stage)
	})

	t.Run("deny beats allow", func(t *testing.T) {
		svc := newService(t)
		svc.SetRules([]Rule{
			{Tool: "bash", Content: "rm -rf:*", Behavior: BehaviorDeny},
			{Tool: "bash", Behavior: BehaviorAllow},
		})
		decision := svc.Decide(context.Background(), req("bash", "rm -rf /"))
		require.True(t, decision.Denied())
		require.Equal(t, StageRuleDeny, decision.Stage)
	})

	t.Run("ask beats allow", func(t *testing.T) {
		svc := newService(t)
		svc.SetRules([]Rule{
			{Tool: "bash", Content: "git push:*", Behavior: BehaviorAsk},
			{Tool: "bash", Behavior: BehaviorAllow},
		})
		decision := svc.Decide(context.Background(), req("bash", "git push origin main"))
		require.True(t, decision.Prompt)
		require.Equal(t, StageRuleAsk, decision.Stage)
	})

	t.Run("allow rule approves without prompting", func(t *testing.T) {
		svc := newService(t)
		svc.SetRules([]Rule{{Tool: "edit", Content: "internal/**", Behavior: BehaviorAllow}})
		decision := svc.Decide(context.Background(), CreatePermissionRequest{
			ToolName: "edit", RuleContent: "internal/agent/agent.go", Mutating: true,
		})
		require.True(t, decision.Allowed())
		require.Equal(t, StageRuleAllow, decision.Stage)
	})

	t.Run("plan mode denies mutating tools", func(t *testing.T) {
		svc := newService(t)
		svc.SetMode(ModePlan)
		decision := svc.Decide(context.Background(), req("edit", "main.go"))
		require.True(t, decision.Denied())
		require.Equal(t, StageModePlan, decision.Stage)
	})

	t.Run("auto mode allows read-only tools and plain commands", func(t *testing.T) {
		svc := newService(t)
		svc.SetMode(ModeAuto)

		readOnly := CreatePermissionRequest{ToolName: "grep", RuleContent: "x", ReadOnly: true}
		decision := svc.Decide(context.Background(), readOnly)
		require.True(t, decision.Allowed())
		require.Equal(t, StageModeAuto, decision.Stage)

		plain := CreatePermissionRequest{ToolName: "bash", RuleContent: "git status", Mutating: true}
		decision = svc.Decide(context.Background(), plain)
		require.True(t, decision.Allowed())
		require.Equal(t, StageAllowlist, decision.Stage)

		dangerous := CreatePermissionRequest{ToolName: "bash", RuleContent: "rm -rf /", Mutating: true}
		decision = svc.Decide(context.Background(), dangerous)
		require.True(t, decision.Prompt)
	})

	t.Run("dontAsk denies what would prompt", func(t *testing.T) {
		svc := newService(t)
		svc.SetMode(ModeDontAsk)
		decision := svc.Decide(context.Background(), req("bash", "rm -rf /"))
		require.True(t, decision.Denied())
		require.Equal(t, StageModeDontAsk, decision.Stage)
	})

	t.Run("bypass approves", func(t *testing.T) {
		svc := newService(t)
		svc.SetMode(ModeBypassPermissions)
		decision := svc.Decide(context.Background(), req("bash", "rm -rf /"))
		require.True(t, decision.Allowed())
		require.Equal(t, StageModeBypass, decision.Stage)
	})

	t.Run("hook approval is honoured", func(t *testing.T) {
		svc := newService(t)
		ctx := WithHookApproval(context.Background(), "c1")
		decision := svc.Decide(ctx, req("bash", "rm -rf /"))
		require.True(t, decision.Allowed())
		require.Equal(t, StageHook, decision.Stage)
	})

	t.Run("unmatched call prompts", func(t *testing.T) {
		svc := newService(t)
		decision := svc.Decide(context.Background(), req("bash", "rm -rf /"))
		require.True(t, decision.Prompt)
		require.Equal(t, StageUser, decision.Stage)
	})
}

func TestRequestRecordsDecision(t *testing.T) {
	t.Parallel()

	svc := NewPermissionService(t.TempDir(), false, nil).(*permissionService)
	svc.SetRules([]Rule{{Tool: "bash", Content: "git status", Behavior: BehaviorDeny}})

	granted, err := svc.Request(context.Background(), CreatePermissionRequest{
		SessionID:   "s1",
		ToolCallID:  "call-1",
		ToolName:    "bash",
		Action:      "run",
		RuleContent: "git status",
		Mutating:    true,
	})
	require.NoError(t, err)
	require.False(t, granted)

	decision, ok := svc.DecisionFor("call-1")
	require.True(t, ok)
	require.Equal(t, StageRuleDeny, decision.Stage)
}
