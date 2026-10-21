package repomap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBuildSelfIndexesTheCrushRepository is a smoke test that indexes
// this repository itself: it must find its core symbols.
func TestBuildSelfIndexesTheCrushRepository(t *testing.T) {
	t.Parallel()
	svc := New("../..")

	g, err := svc.Build(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, g.Files)
	require.Contains(t, g.Symbols, "github.com/charmbracelet/crush/internal/agent.coordinator")
	require.Contains(t, g.Symbols, "github.com/charmbracelet/crush/internal/repomap.New")

	section := svc.PromptSection(g)
	require.Contains(t, section, "<repo-map>")
	require.Contains(t, section, "github.com/charmbracelet/crush")
}

// BenchmarkBuildSelf measures indexing this repository.
func BenchmarkBuildSelf(b *testing.B) {
	svc := New("../..")
	b.ResetTimer()
	for range b.N {
		if _, err := svc.Build(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}
