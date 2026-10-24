package prompt

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAssembleDropsEmptyBlocks(t *testing.T) {
	t.Parallel()

	set := Assemble(
		Block{Name: "one", Text: "first"},
		Block{Name: "empty", Text: "   \n"},
		Block{Name: "two", Text: "second", Dynamic: true},
	)
	require.Len(t, set.Blocks(), 2)
	require.Equal(t, "first\n\nsecond", set.Render())
}

func TestSetStaticAndDynamic(t *testing.T) {
	t.Parallel()

	set := Assemble(
		Block{Name: "instructions", Text: "static"},
		Block{Name: "environment", Text: "<env/>", Dynamic: true},
		Block{Name: "context", Text: "context", Dynamic: true},
	)
	require.Equal(t, "static", set.Static())
	require.Equal(t, "<env/>\n\ncontext", set.Dynamic())
	require.Equal(t, len("static")+2, set.BoundaryOffset())
}

func TestBoundaryOffsetWithNoDynamicBlocks(t *testing.T) {
	t.Parallel()

	set := Assemble(Block{Name: "instructions", Text: "static"})
	require.Equal(t, -1, set.BoundaryOffset())
}

func TestEnvironmentBlock(t *testing.T) {
	t.Parallel()

	block := EnvironmentBlock(PromptDat{
		WorkingDir: "/work",
		IsGitRepo:  true,
		Platform:   "linux",
		Date:       "1/2/2026",
		GitStatus:  "Current branch: main\nStatus: clean\n",
	})
	require.True(t, block.Dynamic)
	require.Contains(t, block.Text, "<env>")
	require.Contains(t, block.Text, "Working directory: /work")
	require.Contains(t, block.Text, "Is directory a git repo: yes")
	require.Contains(t, block.Text, "Platform: linux")
	require.Contains(t, block.Text, "Today's date: 1/2/2026")
	require.Contains(t, block.Text, "Current branch: main")

	clean := EnvironmentBlock(PromptDat{WorkingDir: "/work", Platform: "linux", Date: "1/2/2026"})
	require.Contains(t, clean.Text, "Is directory a git repo: no")
	require.NotContains(t, clean.Text, "Git status")
}

func TestEnvironmentUpdateReplacesEarlierValues(t *testing.T) {
	t.Parallel()

	block := EnvironmentUpdate(PromptDat{
		WorkingDir: "/new",
		IsGitRepo:  true,
		Platform:   "darwin",
		Date:       "1/2/2026",
	}, []string{"the working directory", "whether the directory is a git repository"})

	require.True(t, block.Dynamic)
	require.True(t, strings.HasPrefix(block.Text, "# Environment update"))
	require.Contains(t, block.Text, "replaces what this session was told earlier")
	require.Contains(t, block.Text, "the working directory")
	require.Contains(t, block.Text, "Working directory: /new")
}
