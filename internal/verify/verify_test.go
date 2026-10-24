package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestDetectGo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.27\n")

	profile, err := Detect(root)
	require.NoError(t, err)
	require.Equal(t, KindGo, profile.Kind)
	require.Len(t, profile.Checks, 1)
	require.Equal(t, []string{"go", "test", "./..."}, profile.Checks[0].Argv)
}

func TestDetectNode(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "package.json", `{
  "scripts": {
    "test": "vitest run",
    "typecheck": "tsc --noEmit"
  }
}`)

	profile, err := Detect(root)
	require.NoError(t, err)
	require.Equal(t, KindNode, profile.Kind)
	require.Len(t, profile.Checks, 2)
	require.Equal(t, "test", profile.Checks[0].Name)
	require.Equal(t, "typecheck", profile.Checks[1].Name)
}

func TestDetectNodePlaceholderTest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "package.json", `{
  "scripts": {
    "test": "echo \"Error: no test specified\" && exit 1"
  }
}`)

	_, err := detectNode(root, []byte(`{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`))
	require.ErrorIs(t, err, ErrNoProfile)
}

func TestDetectMake(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "Makefile", "build:\n\tgo build ./...\n\ntest:\n\tgo test ./...\n")

	profile, err := Detect(root)
	require.NoError(t, err)
	require.Equal(t, KindMake, profile.Kind)
	require.Equal(t, []string{"make", "test"}, profile.Checks[0].Argv)
}

func TestDetectUnknown(t *testing.T) {
	t.Parallel()
	_, err := Detect(t.TempDir())
	require.ErrorIs(t, err, ErrNoProfile)
}

func TestDetectMalformedManifestFailsLoud(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "package.json", `{"scripts":`)

	_, err := Detect(root)
	require.ErrorContains(t, err, "failed to parse package.json")
}

func TestSelectGoTargets(t *testing.T) {
	t.Parallel()
	profile := Profile{
		Root:   t.TempDir(),
		Kind:   KindGo,
		Checks: []Check{{Name: "test", Argv: []string{"go", "test", "./..."}}},
	}

	selected, err := Select(profile, nil, []string{"internal/b/b.go", "internal/a/a.go"}, false)
	require.NoError(t, err)
	require.Equal(t, []string{"go", "test", "./internal/a", "./internal/b"}, selected.Checks[0].Argv)
}

func TestSelectGoRootUsesFullSuite(t *testing.T) {
	t.Parallel()
	profile := Profile{
		Root:   t.TempDir(),
		Kind:   KindGo,
		Checks: []Check{{Name: "test", Argv: []string{"go", "test", "./..."}}},
	}

	selected, err := Select(profile, nil, []string{"main.go"}, false)
	require.NoError(t, err)
	require.Equal(t, []string{"go", "test", "./..."}, selected.Checks[0].Argv)
}

func TestSelectRejectsOutsideRepo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	profile := Profile{Root: root, Kind: KindGo}
	_, err := Select(profile, nil, []string{"../other/file.go"}, false)
	require.ErrorContains(t, err, "outside the repository")
}

func TestSelectRustManifests(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "Cargo.toml", "[workspace]\nmembers = [\"crates/a\", \"crates/b\"]\n")
	writeFile(t, root, "crates/a/Cargo.toml", "[package]\nname = \"a\"\n")
	writeFile(t, root, "crates/b/Cargo.toml", "[package]\nname = \"b\"\n")
	writeFile(t, root, "crates/a/src/lib.rs", "pub fn a() {}\n")
	writeFile(t, root, "crates/b/src/lib.rs", "pub fn b() {}\n")

	profile := Profile{
		Root:   root,
		Kind:   KindRust,
		Checks: []Check{{Name: "test", Argv: []string{"cargo", "test"}}},
	}
	selected, err := Select(profile, nil, []string{"crates/a/src/lib.rs", "crates/b/src/lib.rs"}, false)
	require.NoError(t, err)
	require.Len(t, selected.Checks, 2)
	require.Equal(t, []string{"cargo", "test", "--manifest-path", "crates/a/Cargo.toml"}, selected.Checks[0].Argv)
	require.Equal(t, []string{"cargo", "test", "--manifest-path", "crates/b/Cargo.toml"}, selected.Checks[1].Argv)
}

func TestParsePorcelain(t *testing.T) {
	t.Parallel()
	out := []byte(" M internal/a/a.go\x00R  new.go\x00old.go\x00?? web/new.ts\x00")
	require.Equal(t, []string{"internal/a/a.go", "new.go", "web/new.ts"}, parsePorcelain(out))
}

func TestChangedFilesUsesNewRenamePath(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	writeFile(t, root, "old.txt", "content\n")
	run("add", "old.txt")
	run("commit", "-qm", "init")
	run("mv", "old.txt", "new.txt")

	changed := ChangedFiles(root)
	require.Contains(t, changed, "new.txt")
	require.NotContains(t, changed, "old.txt")
}

func TestParseGoFailures(t *testing.T) {
	t.Parallel()
	output := `=== RUN   TestOne
--- FAIL: TestOne (0.00s)
    user_test.go:12: got 1, want 2
--- FAIL: TestTwo (0.00s)
panic: boom
FAIL	example.com/app [build failed]
`
	failures := ParseFailures(KindGo, output)
	require.NotEmpty(t, failures)
	require.Equal(t, "user_test.go", failures[0].File)
	require.Equal(t, 12, failures[0].Line)
	require.Equal(t, "TestOne", failures[0].Test)
	require.Contains(t, failures[0].Message, "got 1")
	require.Contains(t, failures, Failure{Test: "TestTwo", Message: "panic: boom"})
}

func TestParseGoBuildError(t *testing.T) {
	t.Parallel()
	output := "./internal/a/a.go:12:3: undefined: missing\n"
	failures := ParseFailures(KindGo, output)
	require.Len(t, failures, 1)
	require.Equal(t, "internal/a/a.go", failures[0].File)
	require.Equal(t, 12, failures[0].Line)
	require.Contains(t, failures[0].Message, "undefined")
}

func TestParseNodeFailures(t *testing.T) {
	t.Parallel()
	output := `FAIL src/widget.test.ts
  ● Widget renders
    src/widget.ts:12:5 - error TS2304: Cannot find name 'x'.
`
	failures := ParseFailures(KindNode, output)
	require.NotEmpty(t, failures)
	require.Equal(t, "Widget renders", failures[0].Test)
	require.Equal(t, "src/widget.ts", failures[0].File)
	require.Equal(t, 12, failures[0].Line)
	require.Contains(t, failures[0].Message, "TS2304")
}

func TestParsePythonFailures(t *testing.T) {
	t.Parallel()
	output := `FAILED tests/test_api.py::test_users - AssertionError: expected 200
tests/test_api.py:12: AssertionError
`
	failures := ParseFailures(KindPython, output)
	require.NotEmpty(t, failures)
	require.Equal(t, "tests/test_api.py", failures[0].File)
	require.Equal(t, "test_users", failures[0].Test)
	require.Equal(t, 12, failures[0].Line)
}

func TestParseRustFailures(t *testing.T) {
	t.Parallel()
	output := `test core::tests::adds ... FAILED
thread 'core::tests::adds' panicked at src/lib.rs:12:3:
assertion failed
`
	failures := ParseFailures(KindRust, output)
	require.Len(t, failures, 1)
	require.Equal(t, "src/lib.rs", failures[0].File)
	require.Equal(t, 12, failures[0].Line)
	require.Equal(t, "core::tests::adds", failures[0].Test)
}

func TestParseGenericFallback(t *testing.T) {
	t.Parallel()
	output := "src/main.java:42: error: cannot find symbol\n"
	failures := ParseFailures(Kind("other"), output)
	require.Len(t, failures, 1)
	require.Equal(t, "src/main.java", failures[0].File)
	require.Equal(t, 42, failures[0].Line)
}

func TestRunPasses(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not installed")
	}
	result := Run(context.Background(), Profile{
		Root:   t.TempDir(),
		Kind:   KindGo,
		Checks: []Check{{Name: "version", Argv: []string{"go", "version"}}},
	}, 30*time.Second)
	require.True(t, result.Passed())
	require.Len(t, result.Checks, 1)
	require.Equal(t, 0, result.Checks[0].ExitCode)
	require.Contains(t, result.Checks[0].Output, "go version")
}

func TestRunGoProjectFailure(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not installed")
	}
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/failing\n\ngo 1.21\n")
	writeFile(t, root, "calc.go", "package failing\n\nfunc Add(a, b int) int { return a - b }\n")
	writeFile(t, root, "calc_test.go", `package failing

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("got %d, want 5", got)
	}
}
`)

	profile, err := Detect(root)
	require.NoError(t, err)
	result := Run(context.Background(), profile, 60*time.Second)
	require.False(t, result.Passed())
	require.NotEmpty(t, result.Failures)
	require.Equal(t, "calc_test.go", result.Failures[0].File)
	require.Equal(t, "TestAdd", result.Failures[0].Test)
}

func TestTailBufferKeepsTail(t *testing.T) {
	t.Parallel()
	buffer := tailBuffer{max: 10}
	_, err := buffer.Write([]byte("0123456789abcdef"))
	require.NoError(t, err)
	require.True(t, buffer.truncated)
	require.Contains(t, buffer.String(), "abcdef")
	require.NotContains(t, buffer.String(), "0123456789")
}
