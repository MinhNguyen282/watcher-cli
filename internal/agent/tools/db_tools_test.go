package tools

import (
	"context"
	"errors"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/dbinspector"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

type dbTestPermission struct {
	permission.Service
	granted  bool
	requests []permission.CreatePermissionRequest
}

func (p *dbTestPermission) Request(_ context.Context, request permission.CreatePermissionRequest) (bool, error) {
	p.requests = append(p.requests, request)
	return p.granted, nil
}

func TestDBToolsDenyBeforeResolvingSecretsOrConnecting(t *testing.T) {
	t.Parallel()
	for _, name := range []string{DBSchemaToolName, DBQueryToolName} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			permissions := &dbTestPermission{}
			configs := func() []dbinspector.DBConfig {
				return []dbinspector.DBConfig{{Name: "main", Type: "postgres", URL: "$SECRET_URL"}}
			}
			resolve := func(string) (string, error) {
				t.Error("Denied access must not resolve credentials")
				return "", errors.New("secret")
			}
			tool := NewDBSchemaTool(permissions, configs, resolve)
			if name == DBQueryToolName {
				tool = NewDBQueryTool(permissions, configs, resolve)
			}
			ctx := context.WithValue(t.Context(), SessionIDContextKey, "session")
			response, err := tool.Run(ctx, fantasy.ToolCall{ID: "call", Input: `{"db":"main","query":"SELECT 1"}`})
			require.NoError(t, err)
			require.Equal(t, NewPermissionDeniedResponse(), response)
			require.Len(t, permissions.requests, 1)
			require.Equal(t, "db:main", permissions.requests[0].Path)
			require.NotNil(t, permissions.requests[0].Params)
		})
	}
}

func TestDBQueryRejectsWritesBeforePermission(t *testing.T) {
	t.Parallel()
	permissions := &dbTestPermission{}
	tool := NewDBQueryTool(permissions, nil, nil)
	response, err := tool.Run(t.Context(), fantasy.ToolCall{Input: `{"db":"main","query":"DELETE FROM users"}`})
	require.NoError(t, err)
	require.True(t, response.IsError)
	require.Contains(t, response.Content, "rejected")
	require.Empty(t, permissions.requests)
}

func TestDBToolsUnknownDatabase(t *testing.T) {
	t.Parallel()
	tool := NewDBSchemaTool(&dbTestPermission{}, func() []dbinspector.DBConfig { return nil }, nil)
	response, err := tool.Run(t.Context(), fantasy.ToolCall{Input: `{"db":"missing"}`})
	require.NoError(t, err)
	require.True(t, response.IsError)
	require.Contains(t, response.Content, "not configured")
}
