package tools

import (
	"context"
	_ "embed"
	"fmt"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/dbinspector"
	"github.com/charmbracelet/crush/internal/permission"
)

//go:embed db_query.md
var dbQueryDescription string

// DBQueryToolName is the name of the read-only database query tool.
const DBQueryToolName = "db_query"

// DBQueryParams is a guarded, read-only query against a configured
// database.
type DBQueryParams struct {
	DB    string `json:"db" description:"The configured database name"`
	Query string `json:"query" description:"A single read-only SQL statement (SELECT/WITH/SHOW/DESCRIBE/EXPLAIN)"`
}

// NewDBQueryTool returns the guarded read-only query tool.
func NewDBQueryTool(permissions permission.Service, dbs func() []dbinspector.DBConfig, resolve func(string) (string, error)) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		DBQueryToolName,
		dbQueryDescription,
		func(ctx context.Context, params DBQueryParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			guard := dbinspector.DefaultGuard()
			if err := guard.Validate(params.Query); err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Query rejected by the read-only guard: %v", err)), nil
			}
			dbcfg, err := lookupDB(ctx, permissions, call, DBQueryToolName, "query", params.DB, params, dbs, resolve)
			if err != nil {
				if err == errPermissionDenied {
					return NewPermissionDeniedResponse(), nil
				}
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			db, err := dbinspector.Open(dbcfg)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			defer db.Close()

			cols, rows, err := dbinspector.Query(ctx, db, params.Query, guard)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			return fantasy.NewTextResponse(dbinspector.RenderResult(cols, rows)), nil
		},
	)
}
