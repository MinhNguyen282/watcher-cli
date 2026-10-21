package watchdog

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestHelperProcess is the command run by the Watchdog tests. When
// WATCHDOG_HELPER is not set it does nothing (normal test run).
func TestHelperProcess(t *testing.T) {
	if os.Getenv("WATCHDOG_HELPER") != "1" {
		return
	}
	switch os.Getenv("WATCHDOG_SCENARIO") {
	case "go_panic":
		fmt.Println("panic: runtime error: invalid memory address or nil pointer dereference")
		fmt.Println("[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x45f2a1]")
		fmt.Println("")
		fmt.Println("goroutine 1 [running]:")
		fmt.Println("main.crash()")
		fmt.Println("\t/app/service/user.go:142 +0x1a")
		fmt.Println("main.main()")
		fmt.Println("\t/app/main.go:12 +0x45")
		os.Exit(2)
	case "go_test_fail":
		fmt.Println("--- FAIL: TestUser (0.00s)")
		fmt.Println("    user_test.go:55: expected nil, got error")
		fmt.Println("FAIL")
		fmt.Println("FAIL\tgithub.com/acme/app/service\t0.123s")
		fmt.Println("FAIL")
		os.Exit(1)
	case "node_stack":
		fmt.Println("  1) Service fetches users")
		fmt.Println("     TypeError: Cannot read properties of undefined (reading 'map')")
		fmt.Println("      at fetchUsers (/app/service/users.js:42:15)")
		fmt.Println("      at Context.<anonymous> (/app/test/users.spec.js:10:5)")
		os.Exit(1)
	}
	os.Exit(0)
}

// helperCommand builds the command that runs the helper process
// scenario.
func helperCommand(t *testing.T, scenario string) []string {
	t.Helper()
	return []string{
		os.Args[0],
		"-test.run=TestHelperProcess",
		"--",
	}
}

func helperEnv(scenario string) []string {
	return append(os.Environ(),
		"WATCHDOG_HELPER=1",
		"WATCHDOG_SCENARIO="+scenario,
	)
}

func runScenario(t *testing.T, scenario string) []Finding {
	t.Helper()
	var findings []Finding
	w := &Watchdog{
		Command:  helperCommand(t, scenario),
		Env:      helperEnv(scenario),
		Detector: DefaultDetector(),
		OnFinding: func(f Finding) {
			findings = append(findings, f)
		},
	}
	//nolint:errcheck // exit code is the scenario's payload
	w.Run(t.Context())
	return findings
}

func TestWatchdogDetectsGoPanic(t *testing.T) {
	t.Parallel()
	findings := runScenario(t, "go_panic")
	require.NotEmpty(t, findings, "a panic must produce a finding")
	f := findings[0]
	require.Equal(t, SeverityError, f.Severity)
	require.Equal(t, "go_panic", f.Kind)
	require.Contains(t, f.Context, "invalid memory address")

	require.Len(t, f.Locations, 2)
	require.Equal(t, SourceLocation{File: "/app/service/user.go", Line: 142}, f.Locations[0])
}

func TestWatchdogDetectsGoTestFail(t *testing.T) {
	t.Parallel()
	findings := runScenario(t, "go_test_fail")
	require.NotEmpty(t, findings)
	require.Equal(t, "go_test_fail", findings[0].Kind)
	require.Contains(t, findings[0].Context, "user_test.go:55")
}

func TestWatchdogDetectsNodeStack(t *testing.T) {
	t.Parallel()
	findings := runScenario(t, "node_stack")
	require.NotEmpty(t, findings)
	require.Equal(t, "node_test_fail", findings[0].Kind)
	require.Contains(t, findings[0].Context, "TypeError")
	require.Len(t, findings[0].Locations, 2)
	require.Equal(t, SourceLocation{File: "/app/service/users.js", Line: 42}, findings[0].Locations[0])
}

func TestWatchdogCommandEnv(t *testing.T) {
	// The helper scenario only runs with its env vars set; this test
	// just documents the plumbing contract.
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--")
	cmd.Env = helperEnv("go_panic")
	out, err := cmd.CombinedOutput()
	// The helper exits with the scenario's exit code on purpose.
	require.Error(t, err)
	require.Contains(t, string(out), "panic: runtime error")
}

func TestDebounceSuppressesRepeats(t *testing.T) {
	t.Parallel()
	d := DefaultDetector()

	f1 := d.Scan("/app/service/user.go:12:3: undefined: x")
	require.NotNil(t, f1)
	require.Equal(t, "go_compile", f1.Kind)

	// The identical location within the debounce window is suppressed.
	require.Nil(t, d.Scan("/app/service/user.go:12:3: undefined: x"))
}

func TestDebounceExpires(t *testing.T) {
	t.Parallel()
	d := DefaultDetector()
	d.Debounce = time.Millisecond

	require.NotNil(t, d.Scan("/app/service/user.go:12:3: undefined: x"))
	time.Sleep(5 * time.Millisecond)
	require.NotNil(t, d.Scan("/app/service/user.go:12:3: undefined: x"))
}

func TestFlushEmitsPartialBlock(t *testing.T) {
	t.Parallel()
	d := DefaultDetector()

	require.Nil(t, d.Scan("panic: boom"))            // starts collecting
	require.Nil(t, d.Scan("goroutine 1 [running]:")) // still collecting
	f := d.Flush()
	require.NotNil(t, f)
	require.Equal(t, "go_panic", f.Kind)
	require.Contains(t, f.Context, "boom")
}

func TestExtractLocations(t *testing.T) {
	t.Parallel()

	locs := ExtractLocations(`Traceback (most recent call last):
  File "/app/main.py", line 33, in <module>
    run()
  File "/app/main.py", line 21, in run
    raise ValueError("bad")
ValueError: bad`)
	require.Len(t, locs, 2)
	require.Equal(t, SourceLocation{File: "/app/main.py", Line: 33}, locs[0])
	require.Equal(t, SourceLocation{File: "/app/main.py", Line: 21}, locs[1])

	// Dedupe across dialects.
	locs = ExtractLocations("/a/b.go:5:5: err\n\t/a/b.go:5 +0x1a")
	require.Len(t, locs, 1)
	require.Equal(t, "/a/b.go", locs[0].File)
}

func TestAnalysisPrompt(t *testing.T) {
	t.Parallel()
	f := Finding{
		Kind:      "go_panic",
		Severity:  SeverityError,
		Message:   "panic: boom",
		Context:   "panic: boom\n\t/a/b.go:5 +0x1a",
		Locations: []SourceLocation{{File: "/a/b.go", Line: 5}},
	}
	p := AnalysisPrompt(f, "/repo")
	require.Contains(t, p, "panic: boom")
	require.Contains(t, p, "/repo")

	p = FixPrompt(f, "/repo")
	require.Contains(t, p, "/a/b.go:5")
}
