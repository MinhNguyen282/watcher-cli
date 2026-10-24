package verify

import (
	"regexp"
	"strings"
	"time"
)

// Report is the structured form of a verify tool result. Tool cards render
// this model instead of dumping the raw protocol envelope.
type Report struct {
	Status   string
	Profile  string
	Duration time.Duration
	Checks   []ReportCheck
	Failures []Failure
	Output   string
}

// ReportCheck is one rendered check row.
type ReportCheck struct {
	Name     string
	State    string
	Duration time.Duration
	Command  string
	Failed   bool
	TimedOut bool
}

var (
	verifyHeaderPattern = regexp.MustCompile(`<verify\s+([^>]*)>`)
	verifyAttrPattern   = regexp.MustCompile(`(\w+)="([^"]*)"`)
	verifyCheckPattern  = regexp.MustCompile("^- (.+?): (passed|failed|timed out) \\(([^)]+)\\) `(.*)`$")
)

// ParseReport extracts the verify envelope emitted by the verify tool. It
// returns false when the content is not a verify envelope, so renderers can
// fall back to raw output.
func ParseReport(content string) (Report, bool) {
	header := verifyHeaderPattern.FindStringSubmatch(content)
	if header == nil {
		return Report{}, false
	}

	report := Report{}
	for _, attr := range verifyAttrPattern.FindAllStringSubmatch(header[1], -1) {
		switch attr[1] {
		case "status":
			report.Status = attr[2]
		case "profile":
			report.Profile = attr[2]
		case "duration":
			report.Duration, _ = time.ParseDuration(attr[2])
		}
	}

	start := strings.Index(content, header[0])
	if start < 0 {
		start = 0
	}
	section := ""
	for _, raw := range strings.Split(content[start:], "\n") {
		line := strings.TrimSpace(raw)
		switch line {
		case "":
			continue
		case "checks:":
			section = "checks"
			continue
		case "failures:":
			section = "failures"
			continue
		case "output:":
			index := strings.Index(content, "output:")
			if index >= 0 {
				report.Output = strings.TrimSuffix(strings.TrimSpace(content[index+len("output:"):]), "</verify>")
				report.Output = strings.TrimSpace(report.Output)
			}
			return report, true
		case "</verify>":
			return report, true
		}

		switch section {
		case "checks":
			match := verifyCheckPattern.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			check := ReportCheck{
				Name:     match[1],
				State:    match[2],
				Duration: parseDuration(match[3]),
				Command:  match[4],
			}
			check.Failed = check.State == "failed"
			check.TimedOut = check.State == "timed out"
			report.Checks = append(report.Checks, check)
		case "failures":
			failure, ok := parseFailureLine(strings.TrimPrefix(line, "- "))
			if ok {
				report.Failures = append(report.Failures, failure)
			}
		default:
			// Older envelopes omitted the section header. A leading list
			// item that looks like a failure location is still a failure.
			if strings.HasPrefix(line, "- ") {
				if failure, ok := parseFailureLine(strings.TrimPrefix(line, "- ")); ok {
					report.Failures = append(report.Failures, failure)
				}
			}
		}
	}
	return report, true
}

func parseDuration(text string) time.Duration {
	text = strings.TrimSpace(text)
	duration, err := time.ParseDuration(text)
	if err != nil {
		return 0
	}
	return duration
}

func parseFailureLine(line string) (Failure, bool) {
	if line == "" {
		return Failure{}, false
	}
	location, rest, ok := strings.Cut(line, ": ")
	if !ok {
		return Failure{}, false
	}

	var failure Failure
	if bracket := strings.LastIndex(location, " ["); bracket >= 0 && strings.HasSuffix(location, "]") {
		failure.Test = location[bracket+2 : len(location)-1]
		location = location[:bracket]
	}
	if location != "(unknown)" {
		failure.File, failure.Line = splitLocation(location)
	}
	failure.Message = rest
	return failure, true
}

func splitLocation(location string) (string, int) {
	index := strings.LastIndex(location, ":")
	if index < 0 {
		return location, 0
	}
	line := 0
	for _, r := range location[index+1:] {
		if r < '0' || r > '9' {
			return location, 0
		}
		line = line*10 + int(r-'0')
	}
	return location[:index], line
}
