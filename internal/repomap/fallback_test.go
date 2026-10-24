package repomap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPythonFallbackImportAlternatives(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, source, target string }{
		{"plain", "import os\n", "os"},
		{"alias", "import pathlib as path\n", "pathlib"},
		{"dotted", "import os.path\n", "os.path"},
		{"from", "from collections import deque\n", "collections"},
		{"relative", "from . import helpers\n", "."},
		{"indented", "def run():\n    import sys\n", "sys"},
		{"windows_newlines", "import os\r\n", "os"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			graph := NewGraph(t.TempDir())
			require.NotPanics(t, func() { graph.parseFallbackFile(graph.Root, "example.py", []byte(tc.source)) })
			require.Len(t, graph.Edges, 1)
			require.Equal(t, tc.target, graph.Edges[0].To)
			require.Equal(t, EdgeImports, graph.Edges[0].Kind)
		})
	}
}

func TestBuildRepositoryWithMixedPythonImports(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "example.py"), []byte("import os\nfrom pathlib import Path\n\ndef run():\n    pass\n"), 0o644))
	graph, err := New(root).Build(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, graph.Languages["py"])
	require.Len(t, graph.Edges, 2)
	var targets []string
	for _, edge := range graph.Edges {
		targets = append(targets, edge.To)
	}
	require.ElementsMatch(t, []string{"os", "pathlib"}, targets)
}
