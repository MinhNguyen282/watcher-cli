package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/verify"
	"github.com/stretchr/testify/require"
)

func TestFormatVerifyResultFailed(t *testing.T) {
	t.Parallel()
	result := verify.Result{
		Profile: verify.Profile{Kind: verify.KindGo},
		Checks: []verify.CheckResult{{
			Name:     "test",
			Argv:     []string{"go", "test", "./internal/a"},
			ExitCode: 1,
			Duration: 250 * time.Millisecond,
			Output:   "--- FAIL: TestA\n    a_test.go:12: boom\n",
		}},
		Failures: []verify.Failure{{
			File:    "internal/a/a_test.go",
			Line:    12,
			Test:    "TestA",
			Message: "boom",
		}},
		Duration: time.Second,
	}

	out := formatVerifyResult(result)
	require.Contains(t, out, `status="failed"`)
	require.Contains(t, out, "internal/a/a_test.go:12 [TestA]: boom")
	require.Contains(t, out, "--- test ---")
}

func TestFormatVerifyResultPassed(t *testing.T) {
	t.Parallel()
	result := verify.Result{
		Profile: verify.Profile{Kind: verify.KindGo},
		Checks: []verify.CheckResult{{
			Name:     "test",
			Argv:     []string{"go", "test", "./internal/a"},
			ExitCode: 0,
			Duration: 100 * time.Millisecond,
		}},
		Duration: time.Second,
	}

	out := formatVerifyResult(result)
	require.Contains(t, out, `status="passed"`)
	require.NotContains(t, out, "failures:")
	require.NotContains(t, out, "output:")
}

func TestBoundVerifyOutput(t *testing.T) {
	t.Parallel()
	input := strings.Repeat("x", 40<<10)
	out := boundVerifyOutput(input)
	require.LessOrEqual(t, len(out), 24<<10+64)
	require.Contains(t, out, "verify output truncated")
}

type verifyTestPermission struct {
	permission.Service
	granted  bool
	requests []permission.CreatePermissionRequest
}

func (p *verifyTestPermission) Request(_ context.Context, request permission.CreatePermissionRequest) (bool, error) {
	p.requests = append(p.requests, request)
	return p.granted, nil
}

func TestVerifyToolRunsGoChecks(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not installed")
	}

	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	write("go.mod", "module example.com/failing\n\ngo 1.21\n")
	write("calc.go", "package failing\n\nfunc Add(a, b int) int { return a - b }\n")
	write("calc_test.go", `package failing

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("got %d, want 5", got)
	}
}
`)

	permissions := &verifyTestPermission{granted: true}
	tool := NewVerifyTool(permissions, root, nil)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "session")
	response, err := tool.Run(ctx, fantasy.ToolCall{ID: "call", Input: `{}`})
	require.NoError(t, err)
	require.Contains(t, response.Content, `status="failed"`)
	require.Contains(t, response.Content, "TestAdd")
	require.Contains(t, response.Content, "calc_test.go")
	require.Len(t, permissions.requests, 1)
	require.Equal(t, VerifyToolName, permissions.requests[0].ToolName)
	require.Equal(t, root, permissions.requests[0].Path)
}

func TestVerifyToolWithoutManifest(t *testing.T) {
	t.Parallel()
	permissions := &verifyTestPermission{granted: true}
	tool := NewVerifyTool(permissions, t.TempDir(), nil)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "session")
	response, err := tool.Run(ctx, fantasy.ToolCall{ID: "call", Input: `{}`})
	require.NoError(t, err)
	require.Contains(t, response.Content, "No supported project manifest")
	require.Empty(t, permissions.requests)
}

func TestVerifyToolMalformedManifestFailsLoud(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":`), 0o644))

	permissions := &verifyTestPermission{granted: true}
	tool := NewVerifyTool(permissions, root, nil)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "session")
	response, err := tool.Run(ctx, fantasy.ToolCall{ID: "call", Input: `{}`})
	require.NoError(t, err)
	require.Contains(t, response.Content, "failed to parse package.json")
	require.Empty(t, permissions.requests)
}
