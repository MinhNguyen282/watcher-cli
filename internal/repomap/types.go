// Package repomap builds an in-memory symbol graph of the repository:
// files, packages, and their call/import relationships. The graph is
// injected into the coder's system prompt and exposed to the agent via
// the repo_map tool so first-turn navigation does not depend on
// trial-and-error grepping.
//
// The indexer is pure Go (no CGO): Go files are parsed with go/parser
// for high-confidence symbols, other languages fall back to a
// lightweight structural scanner with low confidence.
package repomap

import (
	"strings"
	"time"
)

// Confidence marks how the symbol was discovered.
type Confidence string

const (
	// ConfidenceHigh means the symbol came from a real AST parse.
	ConfidenceHigh Confidence = "high"
	// ConfidenceLow means the symbol came from the regex fallback.
	ConfidenceLow Confidence = "low"
)

// Symbol is one named code entity.
type Symbol struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	File       string     `json:"file"`
	Line       int        `json:"line"`
	Package    string     `json:"package"`
	Confidence Confidence `json:"confidence"`
}

// Edge kinds.
const (
	EdgeCalls      = "calls"
	EdgeImports    = "imports"
	EdgeImplements = "implements"
)

// Edge connects two symbol IDs or file keys.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

// FileInfo describes one indexed file.
type FileInfo struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	HasTests bool   `json:"has_tests"`
	Size     int64  `json:"size"`
	// Stamp is "mtime:size" for cheap change detection.
	Stamp string `json:"stamp"`
}

// Graph is the repository's indexed symbol graph.
type Graph struct {
	Root       string            `json:"root"`
	ModPath    string            `json:"mod_path"`
	BuiltAt    time.Time         `json:"built_at"`
	Files      []FileInfo        `json:"files"`
	Symbols    map[string]Symbol `json:"symbols"`
	Edges      []Edge            `json:"edges"`
	Languages  map[string]int    `json:"languages"`
	byFile     map[string][]string
	pkgSymbols map[string][]string
	adjOut     map[string]map[string]string // symbol -> target -> edge kind
	adjIn      map[string]map[string]string // symbol -> caller -> edge kind
	fileOf     map[string]string            // symbol -> file
}

// NewGraph returns an initialized graph.
func NewGraph(root string) *Graph {
	return &Graph{
		Root:      root,
		Symbols:   make(map[string]Symbol),
		Languages: make(map[string]int),
	}
}

// finalize builds the reverse indexes after symbols and edges are set.
func (g *Graph) finalize() {
	g.byFile = make(map[string][]string)
	g.fileOf = make(map[string]string)
	g.adjOut = make(map[string]map[string]string)
	g.adjIn = make(map[string]map[string]string)
	g.pkgSymbols = make(map[string][]string)

	for id, s := range g.Symbols {
		g.byFile[s.File] = append(g.byFile[s.File], id)
		g.fileOf[id] = s.File
		if s.Package != "" {
			g.pkgSymbols[s.Package] = append(g.pkgSymbols[s.Package], id)
		}
	}
	add := func(adj map[string]map[string]string, from, to, kind string) {
		if from == "" || to == "" {
			return
		}
		m := adj[from]
		if m == nil {
			m = make(map[string]string)
			adj[from] = m
		}
		m[to] = kind
	}
	for _, e := range g.Edges {
		add(g.adjOut, e.From, e.To, e.Kind)
		add(g.adjIn, e.To, e.From, e.Kind)
	}
}

// languageFor maps a file extension to an index language tag.
func languageFor(path string) string {
	ext := path
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		ext = path[i+1:]
	}
	switch strings.ToLower(ext) {
	case "go":
		return "go"
	case "ts", "tsx", "js", "jsx", "mjs", "cjs":
		return "js"
	case "py":
		return "py"
	case "rs":
		return "rs"
	case "java":
		return "java"
	case "c", "h", "cc", "cpp", "hpp", "cxx":
		return "cpp"
	default:
		return ""
	}
}
