package repomap

import (
	"sort"
	"strings"
)

// BlastRadius returns the symbols and files connected to the targets
// within depth hops, ranked by distance then callers-first relevance.
// Targets may be file paths, file keys, symbol IDs, or unique symbol
// names; package/import paths expand to their symbols and files.
func (g *Graph) BlastRadius(targets []string, depth int) ([]Symbol, []string) {
	if depth <= 0 {
		depth = 2
	}
	seen := make(map[string]int)
	queue := make([]string, 0, len(targets))

	seed := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" {
			return
		}
		// Accept plain file paths and file keys.
		if !strings.HasPrefix(t, "file:") {
			if _, ok := g.byFile[t]; ok {
				t = fileKey(t)
			}
		}
		// Accept bare symbol names by matching unique suffixes.
		if _, ok := g.Symbols[t]; !ok && !strings.HasPrefix(t, "file:") {
			if cands := g.lookupName(t); len(cands) == 1 {
				t = cands[0]
			}
		}
		if _, ok := seen[t]; ok || t == "" {
			return
		}
		seen[t] = 0
		queue = append(queue, t)
		// Files also seed their own symbols.
		if strings.HasPrefix(t, "file:") {
			path := strings.TrimPrefix(t, "file:")
			for _, id := range g.byFile[path] {
				if _, ok := seen[id]; !ok {
					seen[id] = 0
					queue = append(queue, id)
				}
			}
		}
		// Package/import paths seed their package's symbols.
		if ids, ok := g.pkgSymbols[t]; ok {
			for _, id := range ids {
				if _, ok := seen[id]; !ok {
					seen[id] = 0
					queue = append(queue, id)
				}
			}
		}
	}
	for _, t := range targets {
		seed(t)
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		dist := seen[cur]
		if dist >= depth {
			continue
		}
		neighbors := append(symbolKeys(g.adjOut[cur]), symbolKeys(g.adjIn[cur])...)
		for _, n := range neighbors {
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = dist + 1
			queue = append(queue, n)
			// Package nodes expand to their symbols.
			if ids, ok := g.pkgSymbols[n]; ok {
				for _, id := range ids {
					if _, ok := seen[id]; !ok {
						seen[id] = dist + 1
						queue = append(queue, id)
					}
				}
			}
		}
	}

	var symbols []Symbol
	var files []string
	fileSeen := make(map[string]bool)
	for id := range seen {
		if strings.HasPrefix(id, "file:") {
			path := strings.TrimPrefix(id, "file:")
			if !fileSeen[path] {
				fileSeen[path] = true
				files = append(files, path)
			}
			continue
		}
		if s, ok := g.Symbols[id]; ok {
			symbols = append(symbols, s)
			if !fileSeen[s.File] {
				fileSeen[s.File] = true
				files = append(files, s.File)
			}
		}
	}
	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].Confidence != symbols[j].Confidence {
			return symbols[i].Confidence == ConfidenceHigh
		}
		return symbols[i].ID < symbols[j].ID
	})
	sort.Strings(files)
	return symbols, files
}

// lookupName finds symbol IDs whose name matches the query.
func (g *Graph) lookupName(name string) []string {
	var out []string
	for id, s := range g.Symbols {
		if s.Name == name || strings.HasSuffix(id, "."+name) {
			out = append(out, id)
		}
	}
	return out
}

// RelatedFiles returns files that share edges with the given file.
func (g *Graph) RelatedFiles(file string) []string {
	_, files := g.BlastRadius([]string{file}, 1)
	return files
}

// Callers returns the symbols that call the given symbol.
func (g *Graph) Callers(symbolID string) []Symbol {
	var out []Symbol
	for caller := range g.adjIn[symbolID] {
		if s, ok := g.Symbols[caller]; ok {
			out = append(out, s)
		}
	}
	sortSymbols(out)
	return out
}

// Callees returns the symbols called by the given symbol.
func (g *Graph) Callees(symbolID string) []Symbol {
	var out []Symbol
	for target := range g.adjOut[symbolID] {
		if s, ok := g.Symbols[target]; ok {
			out = append(out, s)
		}
	}
	sortSymbols(out)
	return out
}

// Implementers returns the symbols asserting the interface.
func (g *Graph) Implementers(ifaceID string) []Symbol {
	var out []Symbol
	for from, kind := range g.adjIn[ifaceID] {
		if kind == EdgeImplements {
			if s, ok := g.Symbols[from]; ok {
				out = append(out, s)
			}
		}
	}
	sortSymbols(out)
	return out
}

// TopSymbols returns the most-connected symbols, used for the prompt
// summary.
func (g *Graph) TopSymbols(limit int) []Symbol {
	type symScore struct {
		s Symbol
		n int
	}
	var scored []symScore
	for id, s := range g.Symbols {
		if s.Confidence != ConfidenceHigh {
			continue
		}
		scored = append(scored, symScore{
			s: s,
			n: len(g.adjOut[id]) + len(g.adjIn[id]),
		})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].n != scored[j].n {
			return scored[i].n > scored[j].n
		}
		return scored[i].s.ID < scored[j].s.ID
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	out := make([]Symbol, len(scored))
	for i, sc := range scored {
		out[i] = sc.s
	}
	return out
}

func symbolKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sortSymbols(s []Symbol) {
	sort.Slice(s, func(i, j int) bool { return s[i].ID < s[j].ID })
}
