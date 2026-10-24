package verify

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseReport(t *testing.T) {
	t.Parallel()

	content := `<verify status="failed" profile="go" duration="1.25s">
checks:
- test: failed (250ms) ` + "`go test ./...`" + `
- lint: passed (10ms) ` + "`go vet ./...`" + `
- slow: timed out (2s) ` + "`go test ./slow`" + `
failures:
- internal/a/a_test.go:12 [TestA]: boom
- (unknown): package failed
output:
--- test ---
boom
</verify>`

	report, ok := ParseReport(content)
	require.True(t, ok)
	require.Equal(t, "failed", report.Status)
	require.Equal(t, "go", report.Profile)
	require.Equal(t, 1250*time.Millisecond, report.Duration)
	require.Len(t, report.Checks, 3)
	require.True(t, report.Checks[0].Failed)
	require.True(t, report.Checks[2].TimedOut)
	require.Equal(t, "go test ./...", report.Checks[0].Command)
	require.Len(t, report.Failures, 2)
	require.Equal(t, "internal/a/a_test.go", report.Failures[0].File)
	require.Equal(t, 12, report.Failures[0].Line)
	require.Equal(t, "TestA", report.Failures[0].Test)
	require.Equal(t, "boom", report.Failures[0].Message)
	require.Equal(t, "package failed", report.Failures[1].Message)
	require.Contains(t, report.Output, "boom")
}

func TestParseReportRejectsNonVerifyContent(t *testing.T) {
	t.Parallel()

	_, ok := ParseReport("just tool output")
	require.False(t, ok)
}

func TestParseReportPassedWithoutFailures(t *testing.T) {
	t.Parallel()

	content := `<verify status="passed" profile="node" duration="100ms">
checks:
- test: passed (100ms) ` + "`npm test`" + `
</verify>`
	report, ok := ParseReport(content)
	require.True(t, ok)
	require.Equal(t, "passed", report.Status)
	require.Empty(t, report.Failures)
	require.Empty(t, report.Output)
}
