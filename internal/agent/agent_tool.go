package agent

import (
	"context"
	_ "embed"
	"errors"
	"strings"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
)

//go:embed templates/agent_tool.md
var agentToolDescription string

type AgentParams struct {
	Prompt string `json:"prompt" description:"The task for the agent to perform"`
	// SubagentType selects the agent shape: "task" (default) researches and
	// reports, "explore" is a read-only variant, "plan" produces an
	// implementation plan, and "fork" inherits this conversation's context.
	SubagentType string `json:"subagent_type,omitempty" description:"Agent shape: task, explore, plan or fork"`
	Name         string `json:"name,omitempty" description:"Optional name for this agent, for reporting"`
}

// Subagent type names.
const (
	SubagentTask    = "task"
	SubagentExplore = "explore"
	SubagentPlan    = "plan"
	SubagentFork    = "fork"
)

// validSubagentTypes is the closed set accepted by the agent tool.
var validSubagentTypes = map[string]bool{
	"":              true,
	SubagentTask:    true,
	SubagentExplore: true,
	SubagentPlan:    true,
	SubagentFork:    true,
}

const (
	AgentToolName = "agent"
)

func (c *coordinator) agentTool(ctx context.Context) (fantasy.AgentTool, error) {
	agents, err := c.subagentSet(ctx)
	if err != nil {
		return nil, err
	}
	return fantasy.NewParallelAgentTool(
		AgentToolName,
		agentToolDescription,
		func(ctx context.Context, params AgentParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Prompt == "" {
				return fantasy.NewTextErrorResponse("prompt is required"), nil
			}

			sessionID := tools.GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, errors.New("session id missing from context")
			}

			agentMessageID := tools.GetMessageFromContext(ctx)
			if agentMessageID == "" {
				return fantasy.ToolResponse{}, errors.New("agent message id missing from context")
			}

			subagentType := strings.ToLower(strings.TrimSpace(params.SubagentType))
			if !validSubagentTypes[subagentType] {
				return fantasy.NewTextErrorResponse(
					"unknown subagent_type " + params.SubagentType + ": use task, explore, plan or fork",
				), nil
			}
			if subagentType == "" {
				subagentType = SubagentTask
			}
			subagent, ok := agents[subagentType]
			if !ok && subagentType == SubagentPlan {
				// The plan agent is built by the coordinator itself, not by
				// subagentSet: building it here would recurse through the
				// agent tool the plan agent also has.
				subagent, ok = c.planSubagent()
			}
			if !ok {
				return fantasy.NewTextErrorResponse("subagent type " + subagentType + " is not available in this session"), nil
			}

			title := "New Agent Session"
			if name := strings.TrimSpace(params.Name); name != "" {
				title = name
			}
			return c.runSubAgent(ctx, subAgentParams{
				Agent:          subagent,
				SessionID:      sessionID,
				AgentMessageID: agentMessageID,
				ToolCallID:     call.ID,
				Prompt:         params.Prompt,
				SessionTitle:   title,
				InheritContext: subagentType == SubagentFork,
			})
		},
	), nil
}

// subagentSet builds the agent variants the agent tool can dispatch to. The
// explore variant is derived from the task agent with its tool list filtered
// to read-only tools, so it cannot mutate the workspace no matter what it is
// asked to do.
//
// The plan agent is deliberately not built here: the plan agent's own tool
// list includes the agent tool, so building it from inside a tool build would
// recurse. planSubagent resolves it from the coordinator's already-built
// agents instead.
func (c *coordinator) subagentSet(ctx context.Context) (map[string]SessionAgent, error) {
	taskCfg, ok := c.cfg.Config().Agents[config.AgentTask]
	if !ok {
		return nil, errors.New("task agent not configured")
	}
	taskAgentPrompt, err := taskPrompt(prompt.WithWorkingDir(c.cfg.WorkingDir()))
	if err != nil {
		return nil, err
	}
	taskAgent, err := c.buildAgent(ctx, taskAgentPrompt, taskCfg, true)
	if err != nil {
		return nil, err
	}

	exploreCfg := taskCfg
	exploreCfg.AllowedTools = readOnlyToolNames(taskCfg.AllowedTools)
	exploreCfg.AllowedMCP = map[string][]string{}
	exploreAgent, err := c.buildAgent(ctx, taskAgentPrompt, exploreCfg, true)
	if err != nil {
		return nil, err
	}

	return map[string]SessionAgent{
		SubagentTask:    taskAgent,
		SubagentExplore: exploreAgent,
		SubagentFork:    taskAgent,
	}, nil
}

// planSubagent returns the coordinator's plan agent, if it was built.
func (c *coordinator) planSubagent() (SessionAgent, bool) {
	c.agentMu.RLock()
	defer c.agentMu.RUnlock()
	agent, ok := c.agents[config.AgentPlan]
	return agent, ok
}

// readOnlyToolNames keeps only tools the harness classifies as read-only.
func readOnlyToolNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if tools.IsReadOnly(name) {
			out = append(out, name)
		}
	}
	return out
}
