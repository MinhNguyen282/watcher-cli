package permission

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Behavior is what a matched rule does.
type Behavior string

const (
	// BehaviorAllow approves a call without prompting.
	BehaviorAllow Behavior = "allow"
	// BehaviorDeny refuses a call without prompting.
	BehaviorDeny Behavior = "deny"
	// BehaviorAsk forces a prompt even when a broader allow rule exists.
	BehaviorAsk Behavior = "ask"
)

// Destination is where a rule is stored. It matters to the UI (which file a
// "always allow" writes to) and to config merging, not to matching.
type Destination string

const (
	DestinationSession         Destination = "session"
	DestinationLocalSettings   Destination = "localSettings"
	DestinationProjectSettings Destination = "projectSettings"
	DestinationUserSettings    Destination = "userSettings"
)

// Rule is a tool-scoped permission rule.
//
// The wire format is "Tool" or "Tool(content)", for example:
//
//	bash
//	bash(git status:*)
//	view(./internal/**)
//	web_fetch(domain:example.com)
//
// Content is optional; an empty content rule matches every call of the tool.
type Rule struct {
	Tool        string      `json:"tool"`
	Content     string      `json:"content,omitempty"`
	Behavior    Behavior    `json:"behavior"`
	Destination Destination `json:"destination,omitempty"`
}

// ParseRule parses one rule spec into a Rule with the given behavior.
func ParseRule(spec string, behavior Behavior, destination Destination) (Rule, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return Rule{}, fmt.Errorf("empty permission rule")
	}
	if behavior != BehaviorAllow && behavior != BehaviorDeny && behavior != BehaviorAsk {
		return Rule{}, fmt.Errorf("unknown rule behavior %q", behavior)
	}
	tool := trimmed
	content := ""
	if open := strings.IndexByte(trimmed, '('); open >= 0 {
		if !strings.HasSuffix(trimmed, ")") {
			return Rule{}, fmt.Errorf("rule %q: missing closing parenthesis", spec)
		}
		tool = strings.TrimSpace(trimmed[:open])
		content = strings.TrimSpace(trimmed[open+1 : len(trimmed)-1])
	}
	if tool == "" {
		return Rule{}, fmt.Errorf("rule %q: tool name is required", spec)
	}
	if strings.ContainsAny(tool, " \t\n") {
		return Rule{}, fmt.Errorf("rule %q: tool name cannot contain whitespace", spec)
	}
	return Rule{
		Tool:        strings.ToLower(tool),
		Content:     content,
		Behavior:    behavior,
		Destination: destination,
	}, nil
}

// String renders the rule back to its spec form.
func (r Rule) String() string {
	if r.Content == "" {
		return r.Tool
	}
	return r.Tool + "(" + r.Content + ")"
}

// Matches reports whether the rule applies to a call. content is the call's
// rule content (file path, command, URL...) as extracted from the tool input.
func (r Rule) Matches(tool, content string) bool {
	if !strings.EqualFold(r.Tool, tool) {
		return false
	}
	if r.Content == "" {
		return true
	}
	return matchRuleContent(r.Tool, r.Content, content)
}

func matchRuleContent(tool, rule, content string) bool {
	rule = strings.TrimSpace(rule)
	content = strings.TrimSpace(content)
	if rule == "" || rule == "*" {
		return true
	}
	switch tool {
	case "bash", "bashoutput", "killshell":
		return matchCommandRule(rule, content)
	case "webfetch", "web_fetch", "websearch", "web_search":
		return matchDomainRule(rule, content)
	default:
		return matchPathRule(rule, content)
	}
}

// matchCommandRule supports exact commands and a trailing "*" wildcard:
// "git status" matches only that command, "git *" matches any git command,
// and a trailing ":*" is treated the same as "*" for compatibility with
// Claude Code style rules.
func matchCommandRule(rule, command string) bool {
	rule = strings.TrimSpace(rule)
	prefix := false
	switch {
	case strings.HasSuffix(rule, ":*"):
		rule = strings.TrimSpace(strings.TrimSuffix(rule, ":*"))
		prefix = true
	case strings.HasSuffix(rule, "*"):
		rule = strings.TrimSpace(strings.TrimSuffix(rule, "*"))
		prefix = true
	}
	if rule == "" {
		return true
	}
	return command == rule || (prefix && strings.HasPrefix(command, rule))
}

// matchDomainRule accepts either "domain:host" or a bare host suffix.
func matchDomainRule(rule, value string) bool {
	host := strings.TrimPrefix(rule, "domain:")
	value = strings.TrimPrefix(value, "domain:")
	// A URL rule matches by prefix; a domain rule matches host suffixes.
	if strings.Contains(host, "://") {
		return strings.HasPrefix(value, host)
	}
	if strings.Contains(value, "://") {
		if parsed, ok := hostOf(value); ok {
			value = parsed
		}
	}
	return value == host || strings.HasSuffix(value, "."+host)
}

func hostOf(raw string) (string, bool) {
	rest := raw
	if idx := strings.Index(rest, "://"); idx >= 0 {
		rest = rest[idx+3:]
	}
	if idx := strings.IndexAny(rest, "/?#"); idx >= 0 {
		rest = rest[:idx]
	}
	if idx := strings.LastIndex(rest, "@"); idx >= 0 {
		rest = rest[idx+1:]
	}
	if idx := strings.Index(rest, ":"); idx >= 0 {
		rest = rest[:idx]
	}
	return rest, rest != ""
}

// matchPathRule supports three forms: an absolute or CWD-relative path that
// must prefix-match, a glob, and the special "//path" form for absolute
// paths. Paths are normalized to forward slashes before matching.
func matchPathRule(rule, value string) bool {
	if value == "" {
		return false
	}
	rulePath := filepath.ToSlash(strings.TrimSpace(rule))
	valuePath := filepath.ToSlash(strings.TrimSpace(value))
	if strings.ContainsAny(rulePath, "*?[") {
		if ok, err := filepath.Match(rulePath, valuePath); err == nil && ok {
			return true
		}
		// "**" means any depth; fall back to a regexp translation.
		if strings.Contains(rulePath, "**") {
			if ok, err := regexp.MatchString(globToRegexp(rulePath), valuePath); err == nil && ok {
				return true
			}
		}
		return false
	}
	rulePath = strings.TrimSuffix(rulePath, "/")
	return valuePath == rulePath ||
		strings.HasPrefix(valuePath, rulePath+"/") ||
		strings.HasSuffix(valuePath, "/"+strings.TrimPrefix(rulePath, "./"))
}

func globToRegexp(glob string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch glob[i] {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '[', ']', '{', '}', '\\':
			b.WriteByte('\\')
			b.WriteByte(glob[i])
		default:
			b.WriteByte(glob[i])
		}
	}
	b.WriteString("$")
	return b.String()
}

// CompileRules parses the three rule lists from configuration. Deny and ask
// rules are compiled before allow rules so the returned slice preserves the
// precedence the evaluator relies on.
func CompileRules(allow, deny, ask []string) ([]Rule, error) {
	out := make([]Rule, 0, len(allow)+len(deny)+len(ask))
	for _, group := range []struct {
		specs    []string
		behavior Behavior
	}{
		{deny, BehaviorDeny},
		{ask, BehaviorAsk},
		{allow, BehaviorAllow},
	} {
		for _, spec := range group.specs {
			rule, err := ParseRule(spec, group.behavior, DestinationProjectSettings)
			if err != nil {
				return nil, err
			}
			out = append(out, rule)
		}
	}
	return out, nil
}

// EvaluateRules returns the first matching rule's behavior. Deny and ask
// rules are checked before allow rules by the caller because precedence is
// a policy decision, not a matching decision.
func EvaluateRules(rules []Rule, tool, content string, behavior Behavior) (Rule, bool) {
	for _, rule := range rules {
		if rule.Behavior != behavior {
			continue
		}
		if rule.Matches(tool, content) {
			return rule, true
		}
	}
	return Rule{}, false
}
