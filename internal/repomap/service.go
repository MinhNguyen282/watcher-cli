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

// fileFragment caches the parse result for one file so an edit only
// reparses that file instead of the whole repository.
type fileFragment struct {
	info    FileInfo
	symbols map[string]Symbol
	edges   []Edge
}

// Service builds, caches, and queries the repository map.
type Service struct {
	root            string
	cachePath       string
	maxSymbols      int
	maxSectionBytes int

	mu          sync.RWMutex
	graph       *Graph
	fragments   map[string]*fileFragment
	globalEdges []Edge

	buildMu sync.Mutex
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

// Build walks and parses the repository. Files whose stamp did not
// change since the previous build are reused.
func (s *Service) Build(ctx context.Context) (*Graph, error) {
	s.buildMu.Lock()
	defer s.buildMu.Unlock()

	g, err := s.buildLocked(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.saveCache(g); err != nil {
		slog.Warn("Failed to cache repository map", "error", err)
	}
	return g, nil
}

// Load returns the current graph, revalidating the on-disk inventory
// first. Unchanged repositories keep the same *Graph pointer; edits
// reparse only the changed files. It never returns an error for an
// unindexable repository: an empty graph is returned instead, because
// the map is a navigation aid, not a hard dependency.
func (s *Service) Load(ctx context.Context) (*Graph, error) {
	s.mu.RLock()
	g := s.graph
	s.mu.RUnlock()
	if g != nil && s.current(g) {
		return g, nil
	}

	s.buildMu.Lock()
	defer s.buildMu.Unlock()

	// Another caller may have refreshed the map while we waited.
	s.mu.RLock()
	g = s.graph
	s.mu.RUnlock()
	if g != nil && s.current(g) {
		return g, nil
	}

	if g == nil {
		cached, err := s.loadCache()
		if err == nil && cached != nil && s.current(cached) {
			fragments, global := fragmentsFromGraph(cached)
			s.mu.Lock()
			s.graph = cached
			s.fragments = fragments
			s.globalEdges = global
			s.mu.Unlock()
			return cached, nil
		}
	}

	built, err := s.buildLocked(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.saveCache(built); err != nil {
		slog.Warn("Failed to cache repository map", "error", err)
	}
	return built, nil
}

// buildLocked performs an incremental build. The caller holds buildMu.
func (s *Service) buildLocked(ctx context.Context) (*Graph, error) {
	files, err := walkRepo(s.root)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	previous := s.fragments
	global := append([]Edge(nil), s.globalEdges...)
	if previous == nil && s.graph != nil {
		previous, global = fragmentsFromGraph(s.graph)
	}
	s.mu.RUnlock()

	modPath := readModPath(s.root)
	fragments := make(map[string]*fileFragment, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if old, ok := previous[file.Path]; ok && old != nil && old.info.Stamp == file.Stamp {
			old.info = file
			fragments[file.Path] = old
			continue
		}
		fragments[file.Path] = s.parseFile(file, modPath)
	}

	g := NewGraph(s.root)
	g.ModPath = modPath
	g.BuiltAt = time.Now()
	for _, file := range files {
		fragment := fragments[file.Path]
		if fragment == nil {
			continue
		}
		g.Files = append(g.Files, file)
		g.Languages[file.Language]++
		for id, symbol := range fragment.symbols {
			g.Symbols[id] = symbol
		}
		g.Edges = append(g.Edges, fragment.edges...)
	}
	g.Edges = append(g.Edges, global...)
	g.finalize()

	s.mu.Lock()
	s.graph = g
	s.fragments = fragments
	s.globalEdges = global
	s.mu.Unlock()
	return g, nil
}

// parseFile parses one file into a standalone fragment.
func (s *Service) parseFile(file FileInfo, modPath string) *fileFragment {
	fragment := &fileFragment{
		info:    file,
		symbols: make(map[string]Symbol),
	}
	data, err := readRepoFile(s.root, file.Path)
	if err != nil {
		return fragment
	}

	g := NewGraph(s.root)
	g.ModPath = modPath
	switch file.Language {
	case "go":
		g.parseGoFile(s.root, file.Path, data)
	default:
		g.parseFallbackFile(s.root, file.Path, data)
	}
	for id, symbol := range g.Symbols {
		if symbol.File == file.Path {
			fragment.symbols[id] = symbol
		}
	}
	fragment.edges = g.Edges
	return fragment
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

// fragmentsFromGraph reconstructs per-file fragments from a cached
// graph so the first edit after a cold start is still incremental.
func fragmentsFromGraph(g *Graph) (map[string]*fileFragment, []Edge) {
	fragments := make(map[string]*fileFragment, len(g.Files))
	for i := range g.Files {
		file := g.Files[i]
		fragments[file.Path] = &fileFragment{
			info:    file,
			symbols: make(map[string]Symbol),
		}
	}
	for id, symbol := range g.Symbols {
		if fragment := fragments[symbol.File]; fragment != nil {
			fragment.symbols[id] = symbol
		}
	}

	var global []Edge
	for _, edge := range g.Edges {
		owner := edgeOwner(g, edge)
		if fragment := fragments[owner]; fragment != nil {
			fragment.edges = append(fragment.edges, edge)
			continue
		}
		global = append(global, edge)
	}
	return fragments, global
}

func edgeOwner(g *Graph, edge Edge) string {
	if strings.HasPrefix(edge.From, "file:") {
		return strings.TrimPrefix(edge.From, "file:")
	}
	if symbol, ok := g.Symbols[edge.From]; ok {
		return symbol.File
	}
	return ""
}

func readModPath(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	mf, err := modfile.Parse("go.mod", data, nil)
	if err != nil || mf.Module == nil {
		return ""
	}
	return mf.Module.Mod.Path
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
	for i := range g.Files {
		g.Files[i].HasTests = IsTestFile(g.Files[i].Path)
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
