package repomap

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/modfile"
)

// Defaults for the prompt section budget.
const (
	DefaultMaxSymbols      = 120
	DefaultMaxSectionBytes = 8_000
)

// Service builds, caches, and queries the repository map.
type Service struct {
	root            string
	cachePath       string
	maxSymbols      int
	maxSectionBytes int

	mu    sync.RWMutex
	graph *Graph
}

// New returns a repository map service for root. The cache lives under
// .watcher/repomap/.
func New(root string) *Service {
	return &Service{
		root:            root,
		cachePath:       filepath.Join(root, ".watcher", "repomap", "graph.json"),
		maxSymbols:      DefaultMaxSymbols,
		maxSectionBytes: DefaultMaxSectionBytes,
	}
}

// Build walks and parses the repository into a fresh graph.
func (s *Service) Build(ctx context.Context) (*Graph, error) {
	files, err := walkRepo(s.root)
	if err != nil {
		return nil, err
	}
	g := NewGraph(s.root)
	g.BuiltAt = time.Now()

	// Detect the Go module path for canonical package IDs.
	if data, err := os.ReadFile(filepath.Join(s.root, "go.mod")); err == nil {
		if mf, err := modfile.Parse("go.mod", data, nil); err == nil && mf.Module != nil {
			g.ModPath = mf.Module.Mod.Path
		}
	}

	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := readRepoFile(s.root, f.Path)
		if err != nil {
			continue
		}
		g.Languages[f.Language]++
		g.Files = append(g.Files, f)
		switch f.Language {
		case "go":
			g.parseGoFile(s.root, f.Path, data)
		default:
			g.parseFallbackFile(s.root, f.Path, data)
		}
	}
	g.finalize()

	s.mu.Lock()
	s.graph = g
	s.mu.Unlock()
	return g, nil
}

// Load returns the cached graph when the repository did not change,
// otherwise it rebuilds. It never returns an error for an unindexable
// repository: an empty graph is returned instead, because the map is a
// navigation aid, not a hard dependency.
func (s *Service) Load(ctx context.Context) (*Graph, error) {
	s.mu.RLock()
	g := s.graph
	s.mu.RUnlock()
	if g != nil {
		return g, nil
	}

	cached, err := s.loadCache()
	if err == nil && cached != nil && s.current(cached) {
		s.mu.Lock()
		s.graph = cached
		s.mu.Unlock()
		return cached, nil
	}

	built, err := s.Build(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.saveCache(built); err != nil {
		slog.Warn("Failed to cache repository map", "error", err)
	}
	return built, nil
}

// current reports whether the cached graph matches the files on disk,
// comparing the cheap mtime:size stamp per file.
func (s *Service) current(cached *Graph) bool {
	files, err := walkRepo(s.root)
	if err != nil {
		return false
	}
	if len(files) != len(cached.Files) {
		return false
	}
	have := make(map[string]FileInfo, len(cached.Files))
	for _, f := range cached.Files {
		have[f.Path] = f
	}
	for _, f := range files {
		if c, ok := have[f.Path]; !ok || c.Stamp != f.Stamp {
			return false
		}
	}
	return true
}

func (s *Service) loadCache() (*Graph, error) {
	data, err := os.ReadFile(s.cachePath)
	if err != nil {
		return nil, err
	}
	var g Graph
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("failed to parse repository map cache: %w", err)
	}
	g.finalize()
	return &g, nil
}

func (s *Service) saveCache(g *Graph) error {
	data, err := json.Marshal(g)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.cachePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.cachePath, data, 0o644)
}

// PromptSection renders the graph summary injected into the system
// prompt. It is bounded by maxSymbols and maxSectionBytes.
func (s *Service) PromptSection(g *Graph) string {
	if g == nil || len(g.Files) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<repo-map>\n")
	sb.WriteString(fmt.Sprintf("Root: %s\n", g.Root))
	if g.ModPath != "" {
		sb.WriteString(fmt.Sprintf("Module: %s\n", g.ModPath))
	}

	langs := make([]string, 0, len(g.Languages))
	for lang := range g.Languages {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	sb.WriteString("Languages: " + strings.Join(langs, ", ") + "\n")

	// Key packages: files with the most symbols.
	pkgCount := make(map[string]int)
	for _, s := range g.Symbols {
		pkgCount[s.Package]++
	}
	type pkgScore struct {
		pkg string
		n   int
	}
	var pkgs []pkgScore
	for pkg, n := range pkgCount {
		pkgs = append(pkgs, pkgScore{pkg, n})
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].n > pkgs[j].n })
	sb.WriteString("Key packages:\n")
	for i, p := range pkgs {
		if i >= 12 {
			break
		}
		sb.WriteString(fmt.Sprintf("  - %s (%d symbols)\n", p.pkg, p.n))
	}

	top := g.TopSymbols(s.maxSymbols)
	if len(top) > 0 {
		sb.WriteString("Notable symbols:\n")
		for i, sym := range top {
			if i >= 20 {
				break
			}
			sb.WriteString(fmt.Sprintf("  - %s [%s] %s:%d\n",
				sym.ID, sym.Kind, sym.File, sym.Line))
		}
	}

	sb.WriteString("Query me with the repo_map tool for blast radius, callers, and implementers.\n")
	sb.WriteString("</repo-map>")

	out := sb.String()
	if len(out) > s.maxSectionBytes {
		out = out[:s.maxSectionBytes]
	}
	return out
}
