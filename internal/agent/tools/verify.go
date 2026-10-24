package tools

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/repomap"
	"github.com/charmbracelet/crush/internal/verify"
)

//go:embed verify.md
var verifyDescription string

// VerifyToolName is the name of the project verification tool.
const VerifyToolName = "verify"

// VerifyParams selects what to verify.
type VerifyParams struct {
	Targets        []string `json:"targets,omitempty" description:"Files or directories to verify. Defaults to changed files."`
	Full           bool     `json:"full,omitempty" description:"Run the full project suite instead of selecting changed targets."`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty" description:"Per-check timeout in seconds (default 120, max 600)."`
}

// VerifyPermissionsParams is the permission payload for a verify run.
type VerifyPermissionsParams struct {
	Targets []string `json:"targets,omitempty"`
	Full    bool     `json:"full,omitempty"`
	Checks  []string `json:"checks"`
}

// NewVerifyTool returns a tool that discovers and runs the project's
// own build and test checks.
func NewVerifyTool(permissions permission.Service, workingDir string, repoMap *repomap.Service) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		VerifyToolName,
		verifyDescription,
		func(ctx context.Context, params VerifyParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			profile, err := verify.Detect(workingDir)
			if err != nil {
				if errors.Is(err, verify.ErrNoProfile) {
					return fantasy.NewTextErrorResponse(
						"No supported project manifest found. Use the bash tool to run the project's own checks."), nil
				}
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}

			var graph *repomap.Graph
			if repoMap != nil {
				loadCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				graph, _ = repoMap.Load(loadCtx)
				cancel()
			}
			profile, err = verify.Select(profile, graph, params.Targets, params.Full)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			if len(profile.Checks) == 0 {
				return fantasy.NewTextErrorResponse(
					"No verification commands were selected. Use the bash tool to run the project's checks."), nil
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for verification")
			}
			commandLines := make([]string, 0, len(profile.Checks))
			for _, check := range profile.Checks {
				commandLines = append(commandLines, strings.Join(check.Argv, " "))
			}
			granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
				SessionID:   sessionID,
				Path:        profile.Root,
				ToolCallID:  call.ID,
				ToolName:    VerifyToolName,
				Action:      "execute",
				Description: "Run project checks: " + strings.Join(commandLines, "; "),
				Params: VerifyPermissionsParams{
					Targets: params.Targets,
					Full:    params.Full,
					Checks:  commandLines,
				},
			})
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if !granted {
				return NewPermissionDeniedResponse(), nil
			}

			timeout := verify.DefaultTimeout
			if params.TimeoutSeconds > 0 {
				timeout = time.Duration(params.TimeoutSeconds) * time.Second
			}
			result := verify.Run(ctx, profile, timeout)
			return fantasy.NewTextResponse(formatVerifyResult(result)), nil
		},
	)
}

func formatVerifyResult(result verify.Result) string {
	var sb strings.Builder
	status := "passed"
	if !result.Passed() {
		status = "failed"
	}
	fmt.Fprintf(&sb, "<verify status=%q profile=%q duration=%s>\n",
		status, result.Profile.Kind, result.Duration.Round(time.Millisecond))

	sb.WriteString("checks:\n")
	for _, check := range result.Checks {
		state := "passed"
		switch {
		case check.TimedOut:
			state = "timed out"
		case check.ExitCode != 0:
			state = "failed"
		}
		fmt.Fprintf(&sb, "- %s: %s (%s) `%s`\n",
			check.Name, state, check.Duration.Round(time.Millisecond), strings.Join(check.Argv, " "))
	}

	if len(result.Failures) > 0 {
		sb.WriteString("failures:\n")
		for _, failure := range result.Failures {
			location := failure.File
			if failure.Line > 0 {
				location = fmt.Sprintf("%s:%d", location, failure.Line)
			}
			if location == "" {
				location = "(unknown)"
			}
			test := ""
			if failure.Test != "" {
				test = fmt.Sprintf(" [%s]", failure.Test)
			}
			fmt.Fprintf(&sb, "- %s%s: %s\n", location, test, failure.Message)
		}
	}

	if !result.Passed() {
		sb.WriteString("output:\n")
		for _, check := range result.Checks {
			if check.ExitCode == 0 && !check.TimedOut {
				continue
			}
			fmt.Fprintf(&sb, "--- %s ---\n%s\n", check.Name, check.Output)
		}
	}
	sb.WriteString("</verify>")
	return boundVerifyOutput(sb.String())
}

func boundVerifyOutput(text string) string {
	const maxBytes = 24 << 10
	if len(text) <= maxBytes {
		return text
	}
	half := maxBytes / 2
	return text[:half] + "\n... [verify output truncated] ...\n" + text[len(text)-half:]
}
