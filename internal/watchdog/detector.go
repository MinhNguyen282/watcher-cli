package watchdog

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Severity classifies a detected problem.
type Severity string

const (
	// SeverityError triggers the analysis/fix flow.
	SeverityError Severity = "error"
	// SeverityWarning is reported but never triggers automatic fixes.
	SeverityWarning Severity = "warning"
)

// Finding is one detected problem with the log context around it.
type Finding struct {
	Severity  Severity         `json:"severity"`
	Kind      string           `json:"kind"`
	Message   string           `json:"message"`
	Locations []SourceLocation `json:"locations"`
	Context   string           `json:"context"`
	At        time.Time        `json:"at"`
}

// Pattern matches one class of error signatures.
type Pattern struct {
	Name     string
	Re       *regexp.Regexp
	Severity Severity
}

// Detector scans a line stream for error signatures. Stack-trace
// blocks are collected across lines, and repeated findings for the
// same location are debounced.
type Detector struct {
	Patterns []Pattern

	// MaxContextLines bounds the captured block around a finding.
	MaxContextLines int
	// Debounce suppresses identical findings for this duration.
	Debounce time.Duration

	mu          sync.Mutex
	recent      []string // ring buffer of recent lines
	collecting  string   // block being collected for a multi-line finding
	collectFrom int      // how many lines remain to collect
	collectKind string
	collectSev  Severity
	lastSeen    map[string]time.Time
}

// DefaultDetector returns a detector with the built-in signature
// table.
func DefaultDetector() *Detector {
	return &Detector{
		Patterns: []Pattern{
			{Name: "go_panic", Re: regexp.MustCompile(`^panic: `), Severity: SeverityError},
			{Name: "go_race", Re: regexp.MustCompile(`WARNING: DATA RACE`), Severity: SeverityError},
			{Name: "go_test_fail", Re: regexp.MustCompile(`^--- FAIL: `), Severity: SeverityError},
			{Name: "go_test_fail_pkg", Re: regexp.MustCompile(`^\s*FAIL\s+\S+`), Severity: SeverityError},
			{Name: "go_compile", Re: regexp.MustCompile(`^([\w./\\-]+\.go):\d+:\d+: .*`), Severity: SeverityError},
			{Name: "node_test_fail", Re: regexp.MustCompile(`^\s+\d+\)\s+\S+`), Severity: SeverityError},
			{Name: "node_assert", Re: regexp.MustCompile(`^\s*(AssertionError|TypeError|ReferenceError|SyntaxError|Error): `), Severity: SeverityError},
			{Name: "python_traceback", Re: regexp.MustCompile(`^Traceback \(most recent call last\):`), Severity: SeverityError},
			{Name: "python_test_fail", Re: regexp.MustCompile(`^FAILED `), Severity: SeverityError},
			{Name: "rust_panic", Re: regexp.MustCompile(`^thread '.*' panicked at `), Severity: SeverityError},
			{Name: "build_error", Re: regexp.MustCompile(`(?i)^(error|fatal)( TS\d+)?: `), Severity: SeverityError},
			{Name: "http_5xx", Re: regexp.MustCompile(`(?i)\bHTTP/\d\.\d" 5\d\d\b`), Severity: SeverityWarning},
			{Name: "oom", Re: regexp.MustCompile(`(?i)\b(out of memory|killed process)\b`), Severity: SeverityError},
		},
		MaxContextLines: 40,
		Debounce:        5 * time.Second,
		lastSeen:        make(map[string]time.Time),
	}
}

// Scan feeds one output line into the detector and returns a finding
// when a complete signature was collected. Repeated findings for the
// same location within the debounce window return nil.
func (d *Detector) Scan(line string) *Finding {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.recent = append(d.recent, line)
	if len(d.recent) > d.MaxContextLines {
		d.recent = d.recent[len(d.recent)-d.MaxContextLines:]
	}

	// Finish collecting a multi-line block?
	if d.collectFrom > 0 {
		d.collectFrom--
		if d.collectFrom == 0 {
			return d.emit(d.collectKind, d.collectSev, strings.Join(d.recent[len(d.recent)-min(d.MaxContextLines, len(d.recent)):], "\n"))
		}
		return nil
	}

	for _, p := range d.Patterns {
		if !p.Re.MatchString(line) {
			continue
		}
		d.collectKind = p.Name
		d.collectSev = p.Severity
		d.collectFrom = d.MaxContextLines
		// Single-line findings (compile errors, HTTP codes) emit
		// immediately; block signatures (panics, tracebacks) wait for
		// the tail lines to arrive.
		if p.Name == "go_compile" || p.Name == "build_error" || p.Name == "http_5xx" {
			d.collectFrom = 0
			return d.emit(p.Name, p.Severity, line)
		}
		// The block's first line is the header; keep collecting.
		d.collectFrom = d.MaxContextLines - 1
		return nil
	}
	return nil
}

// emit builds a finding from a collected block, debouncing repeats.
func (d *Detector) emit(kind string, sev Severity, block string) *Finding {
	locations := ExtractLocations(block)
	key := kind
	if len(locations) > 0 {
		key = fmt.Sprintf("%s:%s:%d", kind, locations[0].File, locations[0].Line)
	}
	if last, ok := d.lastSeen[key]; ok && time.Since(last) < d.Debounce {
		return nil
	}
	d.lastSeen[key] = time.Now()

	return &Finding{
		Severity:  sev,
		Kind:      kind,
		Message:   firstLine(block),
		Locations: locations,
		Context:   block,
		At:        time.Now(),
	}
}

// Flush emits any partially collected block (e.g. process exit before
// the collection window filled).
func (d *Detector) Flush() *Finding {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.collectFrom <= 0 {
		return nil
	}
	kind, sev := d.collectKind, d.collectSev
	d.collectFrom = 0
	return d.emit(kind, sev, strings.Join(d.recent, "\n"))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
