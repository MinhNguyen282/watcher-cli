// Package review implements the local diff reviewer: a static
// checklist scanner plus an LLM review pass informed by the harness
// lessons stored in .watcher/memory/lessons.md.
package review

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Severity classifies a finding.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
)

// Source marks where a finding came from.
type Source string

const (
	// SourceStatic findings come from the deterministic checklist.
	SourceStatic Source = "static"
	// SourceLLM findings come from the model's review pass.
	SourceLLM Source = "llm"
)

// Finding is one issue discovered in a diff.
type Finding struct {
	File       string   `json:"file"`
	Line       int      `json:"line"`
	Severity   Severity `json:"severity"`
	Category   string   `json:"category"`
	Message    string   `json:"message"`
	Suggestion string   `json:"suggestion,omitempty"`
	Source     Source   `json:"source"`
}

// Checker is a deterministic diff scanner.
type Checker struct {
	checks []staticCheck
}

// staticCheck scans added diff lines for one class of problem.
type staticCheck struct {
	re      *regexp.Regexp
	cat     string
	sev     Severity
	msg     string
	suggest string
}

// diffFilePattern matches "+++ b/path/to/file" headers.
var diffFilePattern = regexp.MustCompile(`^\+\+\+ b/(.+)$`)

// DefaultChecker returns the built-in static checklist.
func DefaultChecker() *Checker {
	return &Checker{checks: []staticCheck{
		{
			re:      regexp.MustCompile(`(?i)(api[_-]?key|secret|password|passwd|token)\s*[:=]\s*['"]?[A-Za-z0-9_\-+/=]{16,}`),
			cat:     "secret",
			sev:     SeverityCritical,
			msg:     "Possible hardcoded secret or credential",
			suggest: "Move the value to an environment variable or a secret manager.",
		},
		{
			re:      regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
			cat:     "secret",
			sev:     SeverityCritical,
			msg:     "AWS access key ID found",
			suggest: "Rotate the key immediately and load it from the environment.",
		},
		{
			re:      regexp.MustCompile(`(?i)(begin rsa private key|begin openssh private key)`),
			cat:     "secret",
			sev:     SeverityCritical,
			msg:     "Private key material found",
			suggest: "Remove the key file; store it outside the repository.",
		},
		{
			re:      regexp.MustCompile(`^\+\s*(\w+\.)?(Open|Close|Shutdown|Cancel|Stop)\s*\(\s*\)`),
			cat:     "resources",
			sev:     SeverityMedium,
			msg:     "Resource cleanup call added without a visible defer or error check",
			suggest: "Ensure the resource is closed via defer and the error is handled.",
		},
		{
			re:      regexp.MustCompile(`^\+\s*go\s+func`),
			cat:     "concurrency",
			sev:     SeverityMedium,
			msg:     "New goroutine started",
			suggest: "Verify it has a lifecycle: context cancellation, error propagation, and no data races.",
		},
		{
			re:      regexp.MustCompile(`^\+\s*\.\.\.`),
			cat:     "maintenance",
			sev:     SeverityLow,
			msg:     "Suspicious ellipsis added (leftover marker?)",
			suggest: "Remove leftover review/merge markers.",
		},
		{
			re:      regexp.MustCompile(`^\+\s*(//|#|/\*|\*)?\s*(TODO|FIXME|HACK)\b`),
			cat:     "maintenance",
			sev:     SeverityLow,
			msg:     "New TODO/FIXME introduced",
			suggest: "Prefer a tracked issue over an inline marker.",
		},
		{
			re:      regexp.MustCompile(`(?i)^\+\s*(fmt\.Print|console\.log|print\s*\(|println\s*\()`),
			cat:     "debugging",
			sev:     SeverityLow,
			msg:     "Debug print statement added",
			suggest: "Remove or route through the project logger.",
		},
	}}
}

// Scan runs the static checks over a unified diff and returns the
// findings.
func (c *Checker) Scan(diff string) []Finding {
	var findings []Finding
	currentFile := ""
	lineNo := 0
	for _, line := range strings.Split(diff, "\n") {
		if m := diffFilePattern.FindStringSubmatch(line); m != nil {
			currentFile = m[1]
			lineNo = 0
			continue
		}
		if !strings.HasPrefix(line, "+") || strings.HasPrefix(line, "+++") {
			continue
		}
		lineNo++
		for _, check := range c.checks {
			if check.re.MatchString(line) {
				findings = append(findings, Finding{
					File:       currentFile,
					Line:       lineNo,
					Severity:   check.sev,
					Category:   check.cat,
					Message:    check.msg,
					Suggestion: check.suggest,
					Source:     SourceStatic,
				})
			}
		}
	}
	return findings
}

// BuildPrompt renders the review prompt handed to the LLM, combining
// the harness lessons with the diff.
func BuildPrompt(diff, lessons string) string {
	var sb strings.Builder
	sb.WriteString("You are a meticulous code reviewer. Review the following diff for correctness, bugs, error handling, concurrency issues, and convention violations.\n")
	sb.WriteString("Return a JSON array of findings: [{\"file\": string, \"line\": int, \"severity\": \"critical|high|medium|low\", \"category\": string, \"message\": string, \"suggestion\": string}].\n")
	sb.WriteString("Only report real issues. Prefer fewer, precise findings over many speculative ones.\n\n")
	if strings.TrimSpace(lessons) != "" {
		sb.WriteString("<team-lessons>\n")
		sb.WriteString("Past lessons learned on this repository (apply them when reviewing):\n")
		sb.WriteString(lessons)
		sb.WriteString("\n</team-lessons>\n\n")
	}
	sb.WriteString("<diff>\n")
	sb.WriteString(truncate(diff, maxDiffBytes))
	sb.WriteString("\n</diff>\n")
	return sb.String()
}

// maxDiffBytes bounds the diff sent to the model.
const maxDiffBytes = 32 << 10 // 32 KiB

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... (diff truncated)"
}

// ParseFindings parses the LLM's JSON response with a tolerant
// fallback: a JSON array wins, a fenced ```json block is unwrapped,
// and anything else yields a parse error the caller can surface.
func ParseFindings(response string) ([]Finding, error) {
	text := strings.TrimSpace(response)
	// Unwrap a fenced code block when present.
	if i := strings.Index(text, "```"); i >= 0 {
		rest := text[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			rest = rest[:j]
		}
		text = strings.TrimSpace(rest)
	}
	// The model may wrap the array in an object like {"findings": [...]}.
	var raw struct {
		Findings []Finding `json:"findings"`
	}
	var arr []Finding
	if err := json.Unmarshal([]byte(text), &raw); err == nil && len(raw.Findings) > 0 {
		arr = raw.Findings
	} else if err := json.Unmarshal([]byte(text), &arr); err != nil {
		return nil, fmt.Errorf("could not parse review response: %w", err)
	}
	for i := range arr {
		arr[i].Source = SourceLLM
	}
	return arr, nil
}
