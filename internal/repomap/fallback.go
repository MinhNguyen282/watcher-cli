package repomap

import (
	"regexp"
)

// Fallback scanners for languages without a real parser. Confidence is
// low: these are navigation hints, not ground truth.

var (
	funcDefRe    = regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:async\s+)?(?:function\s+|def\s+|fn\s+)([A-Za-z_][A-Za-z0-9_]*)`)
	classDefRe   = regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:default\s+)?(?:class|interface|struct|enum)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	jsImportRe   = regexp.MustCompile(`(?m)^\s*import\s+(?:.+\s+from\s+)?['"]([^'"]+)['"]`)
	pyImportRe   = regexp.MustCompile(`(?m)^\s*(?:from\s+(\S+)\s+import|import\s+(\S+))`)
	rustUseRe    = regexp.MustCompile(`(?m)^\s*use\s+([\w:]+)`)
	javaImportRe = regexp.MustCompile(`(?m)^\s*import\s+([\w.]+);`)
)

// parseFallbackFile scans a non-Go file with the structural regexes.
func (g *Graph) parseFallbackFile(root, file string, data []byte) {
	lang := languageFor(file)
	text := string(data)
	fileNode := fileKey(file)

	symbols := make(map[string]Symbol)
	for _, m := range funcDefRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[m[2]:m[3]]
		id := symbolID(fileNode, name)
		symbols[id] = Symbol{
			ID:         id,
			Name:       name,
			Kind:       "func",
			File:       file,
			Line:       lineOf(text, m[0]),
			Package:    fileNode,
			Confidence: ConfidenceLow,
		}
	}
	for _, m := range classDefRe.FindAllStringSubmatchIndex(text, -1) {
		name := text[m[2]:m[3]]
		id := symbolID(fileNode, name)
		symbols[id] = Symbol{
			ID:         id,
			Name:       name,
			Kind:       "class",
			File:       file,
			Line:       lineOf(text, m[0]),
			Package:    fileNode,
			Confidence: ConfidenceLow,
		}
	}
	for id, s := range symbols {
		if _, exists := g.Symbols[id]; !exists {
			g.Symbols[id] = s
		}
	}

	// Import edges, per language.
	switch lang {
	case "js":
		for _, m := range jsImportRe.FindAllStringSubmatchIndex(text, -1) {
			target := text[m[2]:m[3]]
			g.Edges = append(g.Edges, Edge{From: fileNode, To: target, Kind: EdgeImports})
		}
	case "py":
		for _, m := range pyImportRe.FindAllStringSubmatchIndex(text, -1) {
			// Unmatched alternative captures have indices of -1.
			var target string
			if m[2] >= 0 {
				target = text[m[2]:m[3]]
			} else if m[4] >= 0 {
				target = text[m[4]:m[5]]
			}
			if target != "" {
				g.Edges = append(g.Edges, Edge{From: fileNode, To: target, Kind: EdgeImports})
			}
		}
	case "rs":
		for _, m := range rustUseRe.FindAllStringSubmatchIndex(text, -1) {
			g.Edges = append(g.Edges, Edge{From: fileNode, To: text[m[2]:m[3]], Kind: EdgeImports})
		}
	case "java":
		for _, m := range javaImportRe.FindAllStringSubmatchIndex(text, -1) {
			g.Edges = append(g.Edges, Edge{From: fileNode, To: text[m[2]:m[3]], Kind: EdgeImports})
		}
	}
}

func lineOf(text string, offset int) int {
	line := 1
	for i := 0; i < offset && i < len(text); i++ {
		if text[i] == '\n' {
			line++
		}
	}
	return line
}
