package repomap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"strings"
)

// parseGoFile parses one Go file into symbols and edges appended to
// the graph. Test files are skipped from symbol collection but
// recorded as present.
func (g *Graph) parseGoFile(root, file string, data []byte) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, data, parser.SkipObjectResolution)
	if err != nil {
		// Files that fail to parse (generated, partial) are skipped:
		// the map is a navigation aid, not a compiler.
		return nil
	}

	pkgPath := g.packagePath(file)
	if strings.HasSuffix(file, "_test.go") {
		return nil
	}

	// Package-level import alias resolution for call edges.
	imports := make(map[string]string) // alias/name -> import path
	for _, imp := range f.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		alias := path.Base(importPath)
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		imports[alias] = importPath
		g.Edges = append(g.Edges, Edge{From: fileKey(file), To: importPath, Kind: EdgeImports})
	}

	addSymbol := func(name, kind string, line int) string {
		id := symbolID(pkgPath, name)
		if _, exists := g.Symbols[id]; !exists {
			g.Symbols[id] = Symbol{
				ID:         id,
				Name:       name,
				Kind:       kind,
				File:       file,
				Line:       line,
				Package:    pkgPath,
				Confidence: ConfidenceHigh,
			}
		}
		return id
	}

	// Declaration pass.
	ast.Inspect(f, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.FuncDecl:
			if d.Name == nil || d.Name.Name == "" {
				return true
			}
			name := d.Name.Name
			kind := "func"
			if recv := receiverName(d.Recv); recv != "" {
				name = recv + "." + name
				kind = "method"
			}
			addSymbol(name, kind, fset.Position(d.Pos()).Line)
		case *ast.TypeSpec:
			if d.Name == nil || d.Name.Name == "" {
				return true
			}
			kind := "type"
			switch d.Type.(type) {
			case *ast.StructType:
				kind = "struct"
			case *ast.InterfaceType:
				kind = "interface"
			}
			addSymbol(d.Name.Name, kind, fset.Position(d.Pos()).Line)
		case *ast.ValueSpec:
			for _, name := range d.Names {
				if name == nil || name.Name == "" || name.Name == "_" {
					continue
				}
				kind := "var"
				if name.Name == strings.ToUpper(name.Name) {
					kind = "const"
				}
				addSymbol(name.Name, kind, fset.Position(name.Pos()).Line)
			}
		}
		return true
	})

	// Call-edge pass, walking each function body with its symbol as the
	// caller.
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Body == nil {
			continue
		}
		caller := symbolID(pkgPath, fd.Name.Name)
		if recv := receiverName(fd.Recv); recv != "" {
			caller = symbolID(pkgPath, recv+"."+fd.Name.Name)
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			g.addCallEdge(caller, pkgPath, imports, call)
			return true
		})
	}

	// Implements assertions: var _ Iface = (*T)(nil).
	ast.Inspect(f, func(n ast.Node) bool {
		d, ok := n.(*ast.ValueSpec)
		if !ok || len(d.Names) != 1 || d.Names[0].Name != "_" || len(d.Values) != 1 {
			return true
		}
		rhs := d.Values[0]
		// (*T)(nil) parses as CallExpr{Fun: ParenExpr{X: StarExpr}}.
		if call, ok := rhs.(*ast.CallExpr); ok {
			rhs = call.Fun
		}
		if paren, ok := rhs.(*ast.ParenExpr); ok {
			rhs = paren.X
		}
		star, ok := rhs.(*ast.StarExpr)
		if !ok {
			return true
		}
		ident, ok := star.X.(*ast.Ident)
		if !ok {
			return true
		}
		iface := ""
		switch t := d.Type.(type) {
		case *ast.Ident:
			iface = symbolID(pkgPath, t.Name)
		case *ast.SelectorExpr:
			if pkgIdent, ok := t.X.(*ast.Ident); ok {
				if importPath, found := imports[pkgIdent.Name]; found {
					iface = symbolID(importPath, t.Sel.Name)
				}
			}
		}
		if iface != "" {
			g.Edges = append(g.Edges, Edge{
				From: symbolID(pkgPath, ident.Name),
				To:   iface,
				Kind: EdgeImplements,
			})
		}
		return true
	})

	return nil
}

// addCallEdge appends a call edge from caller to the resolved target.
func (g *Graph) addCallEdge(caller, pkgPath string, imports map[string]string, call *ast.CallExpr) {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		g.Edges = append(g.Edges, Edge{
			From: caller,
			To:   symbolID(pkgPath, fun.Name),
			Kind: EdgeCalls,
		})
	case *ast.SelectorExpr:
		if fun.Sel == nil {
			return
		}
		switch x := fun.X.(type) {
		case *ast.Ident:
			if importPath, ok := imports[x.Name]; ok {
				g.Edges = append(g.Edges, Edge{
					From: caller,
					To:   symbolID(importPath, fun.Sel.Name),
					Kind: EdgeCalls,
				})
			}
		case *ast.ParenExpr:
			// (*T).Method(...)
			if star, ok := x.X.(*ast.StarExpr); ok {
				if ident, ok := star.X.(*ast.Ident); ok {
					g.Edges = append(g.Edges, Edge{
						From: caller,
						To:   symbolID(pkgPath, ident.Name+"."+fun.Sel.Name),
						Kind: EdgeCalls,
					})
				}
			}
		}
	}
}

// receiverName extracts the receiver type name of a method, handling
// pointer and generic receivers.
func receiverName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	return identName(recv.List[0].Type)
}

func identName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return identName(t.X)
	case *ast.IndexExpr:
		return identName(t.X)
	case *ast.IndexListExpr:
		return identName(t.X)
	default:
		return ""
	}
}

// packagePath resolves the import-style package path for a file.
func (g *Graph) packagePath(file string) string {
	dir := path.Dir(file)
	switch {
	case g.ModPath == "":
		if dir == "." {
			return ""
		}
		return dir
	case dir == ".":
		return g.ModPath
	default:
		return g.ModPath + "/" + dir
	}
}

// symbolID builds the canonical ID for a symbol.
func symbolID(pkgPath, name string) string {
	if pkgPath == "" {
		return name
	}
	return pkgPath + "." + name
}

// fileKey builds the file-level node ID used for import edges.
func fileKey(file string) string {
	return "file:" + file
}
