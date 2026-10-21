package review

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const sampleDiff = `diff --git a/service/user.go b/service/user.go
index 0000000..1111111 100644
--- a/service/user.go
+++ b/service/user.go
@@ -1,5 +1,9 @@
 package service
 
+const apiKey = "AKIAIOSFODNN7EXAMPLE"
+
 func Fetch() error {
+	go func() {
+		fmt.Println("debug: fetching")
+	}()
 	return nil
 }
+// TODO: add caching
diff --git a/README.md b/README.md
index 2222222..3333333 100644
--- a/README.md
+++ b/README.md
@@ -1,2 +1,3 @@
 # Readme
+password = "hunter2secretvalue"
`

func TestScanFindsSecrets(t *testing.T) {
	t.Parallel()
	findings := DefaultChecker().Scan(sampleDiff)

	byCat := make(map[string]int)
	for _, f := range findings {
		byCat[f.Category]++
		require.Equal(t, SourceStatic, f.Source)
	}
	// The apiKey line matches both the generic secret pattern and the
	// AKIA pattern; the password line adds one more.
	require.Equal(t, 3, byCat["secret"], "hardcoded secrets must be found")
}

func TestScanFindsConcurrencyAndDebug(t *testing.T) {
	t.Parallel()
	findings := DefaultChecker().Scan(sampleDiff)

	cats := make(map[string]bool)
	for _, f := range findings {
		cats[f.Category] = true
	}
	require.True(t, cats["concurrency"], "go func must be flagged")
	require.True(t, cats["debugging"], "fmt.Println must be flagged")
	require.True(t, cats["maintenance"], "TODO must be flagged")
}

func TestScanTracksFile(t *testing.T) {
	t.Parallel()
	findings := DefaultChecker().Scan(sampleDiff)
	found := false
	for _, f := range findings {
		if f.Category == "secret" && f.File == "service/user.go" {
			found = true
		}
	}
	require.True(t, found, "findings must carry their file")
}

func TestScanEmptyDiff(t *testing.T) {
	t.Parallel()
	require.Empty(t, DefaultChecker().Scan(""))
}

func TestParseFindingsJSONArray(t *testing.T) {
	t.Parallel()
	raw := `[{"file":"a.go","line":3,"severity":"high","category":"bug","message":"boom"}]`
	findings, err := ParseFindings(raw)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	require.Equal(t, SourceLLM, findings[0].Source)
	require.Equal(t, "high", string(findings[0].Severity))
}

func TestParseFindingsFenced(t *testing.T) {
	t.Parallel()
	raw := "Here are the findings:\n```json\n[{\"file\":\"b.go\",\"line\":7,\"severity\":\"medium\",\"category\":\"style\",\"message\":\"x\"}]\n```"
	findings, err := ParseFindings(raw)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	require.Equal(t, "b.go", findings[0].File)
}

func TestParseFindingsWrappedObject(t *testing.T) {
	t.Parallel()
	raw := `{"findings":[{"file":"c.go","line":1,"severity":"low","category":"style","message":"y"}]}`
	findings, err := ParseFindings(raw)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	require.Equal(t, "c.go", findings[0].File)
}

func TestParseFindingsRejectsGarbage(t *testing.T) {
	t.Parallel()
	_, err := ParseFindings("The diff looks great, no issues at all!")
	require.Error(t, err)
}

func TestBuildPromptIncludesLessonsAndDiff(t *testing.T) {
	t.Parallel()
	prompt := BuildPrompt("+secret line", "## Never log passwords")
	require.Contains(t, prompt, "Never log passwords")
	require.Contains(t, prompt, "+secret line")

	// No lessons → no lessons section.
	prompt = BuildPrompt("+x", "")
	require.NotContains(t, prompt, "team-lessons")
}
