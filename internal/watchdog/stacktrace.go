// Package watchdog tails command output or log files, detects error
// signatures (test failures, panics, stack traces), resolves the
// source locations they mention, and hands the findings to a callback
// for analysis and automatic fixing.
package watchdog

import (
	"regexp"
	"strconv"
)

// SourceLocation is a file:line reference extracted from an error.
type SourceLocation struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// Regexes for the stack trace dialects we understand.
var (
	// Go: "\t/path/pkg/file.go:123 +0x1a" (panic stack frames).
	goFrameRe = regexp.MustCompile(`^\s*(?:created by\s+)?[\w./\-~]+\(0x[0-9a-f]+\)\s*$|^\s*(.+?\.go):(\d+)(?:\s+\+0x[0-9a-f]+)?$`)
	// Go compile/type errors: "file.go:12:3: undefined: x".
	goCompilerRe = regexp.MustCompile(`^(.*?\.go):(\d+)(?::(\d+))?:`)
	// Node/JS/TS: "    at fn (/path/file.js:12:34)".
	nodeFrameRe = regexp.MustCompile(`at .+ \((.+):(\d+):\d+\)`)
	// Python: '  File "/path/file.py", line 12, in fn'.
	pythonFrameRe = regexp.MustCompile(`File "(.+)", line (\d+)`)
	// Generic file:line fallback.
	genericLocRe = regexp.MustCompile(`([\w./\\-]+\.(?:go|js|ts|tsx|jsx|py|rs|java|c|cc|cpp|h|hpp)):(\d+)`)
)

// ExtractLocations pulls source locations out of a block of text, in
// order of specificity. The result is deduplicated.
func ExtractLocations(text string) []SourceLocation {
	seen := make(map[SourceLocation]bool)
	var out []SourceLocation
	add := func(file string, line int) {
		if file == "" || line <= 0 {
			return
		}
		loc := SourceLocation{File: file, Line: line}
		if seen[loc] {
			return
		}
		seen[loc] = true
		out = append(out, loc)
	}

	// Go runtime frames come first: they carry the exact file of the
	// panicking goroutine.
	for _, m := range goFrameRe.FindAllStringSubmatch(text, -1) {
		if len(m) >= 3 && m[1] != "" {
			if line, err := strconv.Atoi(m[2]); err == nil {
				add(m[1], line)
			}
		}
	}
	for _, m := range goCompilerRe.FindAllStringSubmatch(text, -1) {
		if line, err := strconv.Atoi(m[2]); err == nil {
			add(m[1], line)
		}
	}
	for _, m := range nodeFrameRe.FindAllStringSubmatch(text, -1) {
		if line, err := strconv.Atoi(m[2]); err == nil {
			add(m[1], line)
		}
	}
	for _, m := range pythonFrameRe.FindAllStringSubmatch(text, -1) {
		if line, err := strconv.Atoi(m[2]); err == nil {
			add(m[1], line)
		}
	}
	for _, m := range genericLocRe.FindAllStringSubmatch(text, -1) {
		if line, err := strconv.Atoi(m[2]); err == nil {
			add(m[1], line)
		}
	}
	return out
}
