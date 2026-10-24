package tools

import (
	"context"
	_ "embed"
	"fmt"
	"path/filepath"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/dbinspector"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/permission"
)

//go:embed openapi_spec.md
var openAPISpecDescription string

// OpenAPISpecToolName is the name of the OpenAPI spec tool.
const OpenAPISpecToolName = "openapi_spec"

// OpenAPISpecParams selects the spec file to summarize.
type OpenAPISpecParams struct {
	Path string `json:"path" description:"Path to an OpenAPI/Swagger JSON or YAML file"`
}

// NewOpenAPISpecTool returns the OpenAPI spec summarizer tool.
func NewOpenAPISpecTool(permissions permission.Service, workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		OpenAPISpecToolName,
		openAPISpecDescription,
		func(ctx context.Context, params OpenAPISpecParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			path, err := fsext.Expand(params.Path)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("error expanding path: %v", err)), nil
			}
			path = filepath.Clean(path)
			if !filepath.IsAbs(path) {
				path = filepath.Join(workingDir, path)
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for reading spec files")
			}
			outcome, err := RequestApproval(ctx, permissions, permission.CreatePermissionRequest{
				SessionID:   sessionID,
				Path:        path,
				ToolCallID:  call.ID,
				ToolName:    OpenAPISpecToolName,
				Action:      "read",
				Description: fmt.Sprintf("Read OpenAPI spec: %s", params.Path),
			})
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if outcome != ApprovalAllowedOnce {
				return NewPermissionDeniedResponse(), nil
			}

			summary, err := dbinspector.LoadOpenAPI(path)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			return fantasy.NewTextResponse(dbinspector.RenderSpec(summary)), nil
		},
	)
}
