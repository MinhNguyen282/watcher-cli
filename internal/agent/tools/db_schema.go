package tools

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/dbinspector"
	"github.com/charmbracelet/crush/internal/permission"
)

//go:embed db_schema.md
var dbSchemaDescription string

// DBSchemaToolName is the name of the database schema tool.
const DBSchemaToolName = "db_schema"

// DBSchemaParams selects the database to introspect.
type DBSchemaParams struct {
	DB string `json:"db" description:"The configured database name (see db add in crushrc)"`
}

// NewDBSchemaTool returns the read-only schema introspection tool.
func NewDBSchemaTool(permissions permission.Service, dbs func() []dbinspector.DBConfig, resolve func(string) (string, error)) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		DBSchemaToolName,
		dbSchemaDescription,
		func(ctx context.Context, params DBSchemaParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			dbcfg, err := lookupDB(ctx, permissions, call, DBSchemaToolName, "schema", params.DB, params, dbs, resolve)
			if err != nil {
				if errors.Is(err, errPermissionDenied) {
					return NewPermissionDeniedResponse(), nil
				}
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			db, err := dbinspector.Open(dbcfg)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			defer db.Close()

			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			tables, err := dbinspector.Introspect(ctx, db, dbcfg.Type)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			return fantasy.NewTextResponse(dbinspector.RenderSchema(tables)), nil
		},
	)
}

// lookupDB resolves a database config by name, requesting permission.
func lookupDB(
	ctx context.Context,
	permissions permission.Service,
	call fantasy.ToolCall,
	toolName, action, name string,
	params any,
	dbs func() []dbinspector.DBConfig,
	resolve func(string) (string, error),
) (dbinspector.DBConfig, error) {
	var dbcfg dbinspector.DBConfig
	found := false
	for _, c := range dbs() {
		if c.Name == name {
			dbcfg = c
			found = true
			break
		}
	}
	if !found {
		return dbcfg, fmt.Errorf("database %q is not configured (declare it with `db add %s --type postgres|mysql --url ...`)", name, name)
	}
	if dbcfg.URL == "" {
		return dbcfg, fmt.Errorf("database %q has no URL configured", name)
	}

	sessionID := GetSessionFromContext(ctx)
	if sessionID == "" {
		return dbcfg, fmt.Errorf("session ID is required for database access")
	}
	granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
		SessionID:   sessionID,
		Path:        "db:" + name,
		ToolCallID:  call.ID,
		ToolName:    toolName,
		Action:      action,
		Params:      params,
		Description: fmt.Sprintf("Read-only database access to %q", name),
	})
	if err != nil {
		return dbcfg, err
	}
	if !granted {
		return dbcfg, errPermissionDenied
	}
	if resolve != nil {
		url, err := resolve(dbcfg.URL)
		if err != nil {
			return dbcfg, fmt.Errorf("failed to resolve database URL")
		}
		dbcfg.URL = url
	}
	return dbcfg, nil
}

var errPermissionDenied = fmt.Errorf("user denied database permission")
