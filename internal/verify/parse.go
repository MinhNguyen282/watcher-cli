package verify

import (
	"regexp"
	"strconv"
	"strings"
)

// Failure is one parsed test or build failure.
type Failure struct {
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Test    string `json:"test,omitempty"`
	Message string `json:"message"`
}

var (
	goFailureLoc = regexp.MustCompile(`^\s+(\S+\.go):(\d+):\s*(.+)$`)
	goBuildLoc   = regexp.MustCompile(`^(\S+\.go):(\d+):(\d+):\s*(.+)$`)

	nodeFailFile = regexp.MustCompile(`^FAIL\s+(\S+)$`)
	nodeTestLine = regexp.MustCompile(`^●\s+(.+)$`)
	nodeTSLoc    = regexp.MustCompile(`^(\S+\.(?:ts|tsx|js|jsx|mjs|cjs))\((\d+),(\d+)\):\s*(?:error\s+)?(.+)$`)
	nodeLoc      = regexp.MustCompile(`^(\S+\.(?:ts|tsx|js|jsx|mjs|cjs)):(\d+):(\d+)\s+-?\s*(.+)$`)
	nodeLocNoCol = regexp.MustCompile(`^(\S+\.(?:ts|tsx|js|jsx|mjs|cjs)):(\d+):\s*(.+)$`)

	pytestFAILED = regexp.MustCompile(`^FAILED\s+(\S+?)(?:::(\S+))?(?:\s+-\s+(.+))?$`)
	pytestLoc    = regexp.MustCompile(`^(\S+\.py):(\d+):\s*(.+)$`)

	rustTestHeader = regexp.MustCompile(`^test\s+(\S+)\s+\.\.\.\s+FAILED$`)
	rustPanic      = regexp.MustCompile(`panicked at ([^:\s]+):(\d+):(\d+)`)
	rustError      = regexp.MustCompile(`^error(?:\[E\d+\])?:\s*(.+)$`)

	genericLoc = regexp.MustCompile(`^(\S+\.(?:go|rs|py|ts|tsx|js|jsx|java|c|cc|cpp|h|hpp)):(\d+)(?::(\d+))?:\s*(.+)$`)
)

// ParseFailures extracts bounded, structured failures from check output.
func ParseFailures(kind Kind, output string) []Failure {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	var failures []Failure
	switch kind {
	case KindGo:
		failures = parseGo(lines)
	case KindNode:
		failures = parseNode(lines)
	case KindPython:
		failures = parsePython(lines)
	case KindRust:
		failures = parseRust(lines)
	}
	if len(failures) == 0 {
		failures = parseGeneric(lines)
	}
	return bounded(dedupeFailures(failures), 12)
}

func parseGo(lines []string) []Failure {
	var failures []Failure
	current := ""
	var failed []string
	seenFailed := make(map[string]bool)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--- FAIL: ") {
			fields := strings.Fields(strings.TrimPrefix(trimmed, "--- FAIL: "))
			if len(fields) > 0 {
				current = fields[0]
				if !seenFailed[current] {
					seenFailed[current] = true
					failed = append(failed, current)
				}
			}
			continue
		}
		if strings.HasPrefix(trimmed, "panic: ") {
			failures = append(failures, Failure{Test: current, Message: trimmed})
			continue
		}
		if match := goFailureLoc.FindStringSubmatch(line); match != nil {
			failures = append(failures, Failure{
				File:    cleanFile(match[1]),
				Line:    atoi(match[2]),
				Test:    current,
				Message: strings.TrimSpace(match[3]),
			})
			continue
		}
		if match := goBuildLoc.FindStringSubmatch(trimmed); match != nil {
			failures = append(failures, Failure{
				File:    cleanFile(match[1]),
				Line:    atoi(match[2]),
				Test:    current,
				Message: strings.TrimSpace(match[4]),
			})
		}
	}

	for _, test := range failed {
		if !hasTestFailure(failures, test) {
			failures = append(failures, Failure{Test: test, Message: "test failed"})
		}
	}
	return failures
}

func parseNode(lines []string) []Failure {
	var failures []Failure
	current := ""
	var failed []string
	seenFailed := make(map[string]bool)

	markFailed := func(name string) {
		if name == "" || seenFailed[name] {
			return
		}
		seenFailed[name] = true
		failed = append(failed, name)
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if match := nodeFailFile.FindStringSubmatch(trimmed); match != nil {
			current = match[1]
			continue
		}
		if match := nodeTestLine.FindStringSubmatch(trimmed); match != nil {
			current = strings.TrimSpace(match[1])
			markFailed(current)
			continue
		}
		if match := nodeTSLoc.FindStringSubmatch(trimmed); match != nil {
			failures = append(failures, Failure{
				File:    cleanFile(match[1]),
				Line:    atoi(match[2]),
				Test:    current,
				Message: strings.TrimSpace(match[4]),
			})
			continue
		}
		if match := nodeLoc.FindStringSubmatch(trimmed); match != nil {
			failures = append(failures, Failure{
				File:    cleanFile(match[1]),
				Line:    atoi(match[2]),
				Test:    current,
				Message: strings.TrimSpace(match[4]),
			})
			continue
		}
		if match := nodeLocNoCol.FindStringSubmatch(trimmed); match != nil {
			failures = append(failures, Failure{
				File:    cleanFile(match[1]),
				Line:    atoi(match[2]),
				Test:    current,
				Message: strings.TrimSpace(match[3]),
			})
		}
	}

	for _, test := range failed {
		if !hasTestFailure(failures, test) {
			failures = append(failures, Failure{Test: test, Message: "test failed"})
		}
	}
	return failures
}

func parsePython(lines []string) []Failure {
	var failures []Failure
	current := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if match := pytestFAILED.FindStringSubmatch(trimmed); match != nil {
			failure := Failure{
				File:    cleanFile(match[1]),
				Test:    match[2],
				Message: strings.TrimSpace(match[3]),
			}
			if failure.Message == "" {
				failure.Message = "test failed"
			}
			current = failure.Test
			failures = append(failures, failure)
			continue
		}
		if match := pytestLoc.FindStringSubmatch(trimmed); match != nil {
			failures = append(failures, Failure{
				File:    cleanFile(match[1]),
				Line:    atoi(match[2]),
				Test:    current,
				Message: strings.TrimSpace(match[3]),
			})
		}
	}
	return failures
}

func parseRust(lines []string) []Failure {
	var failures []Failure
	current := ""
	var failed []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if match := rustTestHeader.FindStringSubmatch(trimmed); match != nil {
			current = match[1]
			failed = append(failed, current)
			continue
		}
		if match := rustPanic.FindStringSubmatch(trimmed); match != nil {
			failures = append(failures, Failure{
				File:    cleanFile(match[1]),
				Line:    atoi(match[2]),
				Test:    current,
				Message: "panic",
			})
			continue
		}
		if match := rustError.FindStringSubmatch(trimmed); match != nil {
			failures = append(failures, Failure{
				Test:    current,
				Message: strings.TrimSpace(match[1]),
			})
		}
	}
	for _, test := range failed {
		if !hasTestFailure(failures, test) {
			failures = append(failures, Failure{Test: test, Message: "test failed"})
		}
	}
	return failures
}

func parseGeneric(lines []string) []Failure {
	var failures []Failure
	for _, line := range lines {
		match := genericLoc.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		failures = append(failures, Failure{
			File:    cleanFile(match[1]),
			Line:    atoi(match[2]),
			Message: strings.TrimSpace(match[4]),
		})
	}
	return failures
}

func dedupeFailures(in []Failure) []Failure {
	var out []Failure
	index := make(map[string]int)
	for _, failure := range in {
		key := failure.File + "\x00" + failure.Test
		if failure.File == "" && failure.Test == "" {
			out = append(out, failure)
			continue
		}
		if position, ok := index[key]; ok {
			existing := &out[position]
			if existing.Line == 0 && failure.Line != 0 {
				existing.Line = failure.Line
			}
			if existing.Message == "" || existing.Message == "test failed" {
				if failure.Message != "" {
					existing.Message = failure.Message
				}
			}
			continue
		}
		index[key] = len(out)
		out = append(out, failure)
	}
	return out
}

func hasTestFailure(failures []Failure, test string) bool {
	if test == "" {
		return false
	}
	for _, failure := range failures {
		if failure.Test == test {
			return true
		}
	}
	return false
}

func bounded(failures []Failure, limit int) []Failure {
	if len(failures) > limit {
		return failures[:limit]
	}
	return failures
}

func cleanFile(file string) string {
	file = strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(file), "\\", "/"), "./")
	return file
}

func atoi(value string) int {
	n, _ := strconv.Atoi(value)
	return n
}
