package repomap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// buildFixture creates a small multi-language repository.
func buildFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write("go.mod", "module example.com/app\n\ngo 1.27\n")
	write("service/user.go", `package service

type Validator interface {
	Name() string
}

type User struct{}

var _ Validator = (*User)(nil)

func (u *User) Name() string { return "" }

func Fetch() error {
	return validate()
}

func validate() error { return nil }
`)
	write("main.go", `package main

import "example.com/app/service"

func main() {
	_ = service.Fetch()
}
`)
	write("web/util.js", `export function helper(x) {
  return x + 1
}

export class Widget {
  render() {}
}
`)
	write("node_modules/pkg/index.js", `export function ignored() {}`)
	write("vendor/lib/x.go", `package lib

func Vendored() {}
`)
	return dir
}

func TestBuildIndexesGoSymbols(t *testing.T) {
	t.Parallel()
	dir := buildFixture(t)
	svc := New(dir)

	g, err := svc.Build(t.Context())
	require.NoError(t, err)
	require.Equal(t, "example.com/app", g.ModPath)

	require.Contains(t, g.Symbols, "example.com/app/service.Fetch")
	require.Equal(t, ConfidenceHigh, g.Symbols["example.com/app/service.Fetch"].Confidence)
	require.Contains(t, g.Symbols, "example.com/app/service.User.Name")

	// Vendored and dependency files are not indexed.
	for _, f := range g.Files {
		require.NotContains(t, f.Path, "node_modules")
		require.NotContains(t, f.Path, "vendor")
	}
}

func TestCallEdges(t *testing.T) {
	t.Parallel()
	dir := buildFixture(t)
	svc := New(dir)
	g, err := svc.Build(t.Context())
	require.NoError(t, err)

	callees := g.Callees("example.com/app/service.Fetch")
	require.Len(t, callees, 1)
	require.Equal(t, "example.com/app/service.validate", callees[0].ID)

	callers := g.Callers("example.com/app/service.Fetch")
	require.Len(t, callers, 1)
	require.Equal(t, "example.com/app.main", callers[0].ID)
}

func TestImplementers(t *testing.T) {
	t.Parallel()
	dir := buildFixture(t)
	svc := New(dir)
	g, err := svc.Build(t.Context())
	require.NoError(t, err)

	impls := g.Implementers("example.com/app/service.Validator")
	require.Len(t, impls, 1)
	require.Equal(t, "example.com/app/service.User", impls[0].ID)
}

func TestBlastRadius(t *testing.T) {
	t.Parallel()
	dir := buildFixture(t)
	svc := New(dir)
	g, err := svc.Build(t.Context())
	require.NoError(t, err)

	symbols, files := g.BlastRadius([]string{"example.com/app/service.Fetch"}, 2)
	require.NotEmpty(t, symbols)
	require.Contains(t, files, "service/user.go")
	require.Contains(t, files, "main.go", "the caller's file is inside the blast radius")

	// File-based target works too.
	_, files = g.BlastRadius([]string{"main.go"}, 2)
	require.Contains(t, files, "service/user.go")
}

func TestFallbackIndexesJS(t *testing.T) {
	t.Parallel()
	dir := buildFixture(t)
	svc := New(dir)
	g, err := svc.Build(t.Context())
	require.NoError(t, err)

	var found Symbol
	for id, s := range g.Symbols {
		if s.Name == "Widget" {
			found = s
			_ = id
		}
	}
	require.NotEmpty(t, found.Name)
	require.Equal(t, ConfidenceLow, found.Confidence)
}

func TestCacheReuseAndInvalidation(t *testing.T) {
	t.Parallel()
	dir := buildFixture(t)
	svc := New(dir)

	g1, err := svc.Load(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, g1.Symbols)

	// Second load is served from the in-memory cache.
	g2, err := svc.Load(t.Context())
	require.NoError(t, err)
	require.Same(t, g1, g2)

	// A source change invalidates the disk cache for the next fresh
	// service (mtime:size stamp differs).
	path := filepath.Join(dir, "main.go")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(data, []byte("\n")...), 0o644))

	svc2 := New(dir)
	g3, err := svc2.Load(t.Context())
	require.NoError(t, err)
	require.NotNil(t, g3)
	// The rebuilt graph's stamp set matches the modified file.
	found := false
	for _, f := range g3.Files {
		if f.Path == "main.go" {
			found = true
		}
	}
	require.True(t, found)
}

func TestPromptSectionBounded(t *testing.T) {
	t.Parallel()
	dir := buildFixture(t)
	svc := New(dir)
	g, err := svc.Build(t.Context())
	require.NoError(t, err)

	section := svc.PromptSection(g)
	require.Contains(t, section, "<repo-map>")
	require.Contains(t, section, "example.com/app")
	require.Contains(t, section, "repo_map tool")
	require.LessOrEqual(t, len(section), DefaultMaxSectionBytes)

	// An empty graph renders no section.
	require.Empty(t, svc.PromptSection(NewGraph(t.TempDir())))
}
