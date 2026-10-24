package prompt

import (
	"fmt"
	"strings"
)

// DateFormat is the date format used in the environment record. It is
// exported so a mid-session environment update renders the same shape.
const DateFormat = "1/2/2006"

// CacheScope is the provider cache scope of a prompt block. An empty scope
// means the block is cached with the surrounding prefix.
type CacheScope string

const (
	// ScopeNone means "cache with the rest of the prefix".
	ScopeNone CacheScope = ""
	// ScopeOrg is content shared by everyone in the organization.
	ScopeOrg CacheScope = "org"
	// ScopeGlobal is content shared by every session on this machine.
	ScopeGlobal CacheScope = "global"
)

// Block is one named section of the system prompt. Blocks are rendered in
// order: every static block first, then every dynamic block. Volatile
// content therefore never invalidates the cached prefix.
type Block struct {
	Name    string
	Text    string
	Dynamic bool
	Scope   CacheScope
}

// Set is an ordered, rendered system prompt split at the static/dynamic
// boundary.
type Set struct {
	blocks []Block
}

// Assemble drops empty blocks and returns a Set in rendering order.
func Assemble(blocks ...Block) Set {
	out := make([]Block, 0, len(blocks))
	for _, block := range blocks {
		if strings.TrimSpace(block.Text) == "" {
			continue
		}
		out = append(out, block)
	}
	return Set{blocks: out}
}

// Blocks returns the retained blocks.
func (s Set) Blocks() []Block { return s.blocks }

// Render joins the blocks into one system prompt.
func (s Set) Render() string {
	parts := make([]string, 0, len(s.blocks))
	for _, block := range s.blocks {
		parts = append(parts, strings.TrimSpace(block.Text))
	}
	return strings.Join(parts, "\n\n")
}

// Static renders only the cacheable prefix.
func (s Set) Static() string { return s.renderWhere(false) }

// Dynamic renders only the per-turn tail.
func (s Set) Dynamic() string { return s.renderWhere(true) }

func (s Set) renderWhere(dynamic bool) string {
	parts := make([]string, 0, len(s.blocks))
	for _, block := range s.blocks {
		if block.Dynamic != dynamic {
			continue
		}
		parts = append(parts, strings.TrimSpace(block.Text))
	}
	return strings.Join(parts, "\n\n")
}

// BoundaryOffset reports the byte offset where the dynamic tail begins in
// Render output, or -1 when every block is static.
func (s Set) BoundaryOffset() int {
	static := s.Static()
	hasDynamic := false
	for _, block := range s.blocks {
		if block.Dynamic && strings.TrimSpace(block.Text) != "" {
			hasDynamic = true
			break
		}
	}
	if !hasDynamic {
		return -1
	}
	return len(static) + 2 // two newlines between the prefix and the tail
}

// EnvironmentBlock renders the structured environment record. It is the one
// block that changes during a session, so it stays at the end of the prompt
// and is also what EnvironmentUpdate diffs against.
func EnvironmentBlock(d PromptDat) Block {
	var b strings.Builder
	b.WriteString("<env>\n")
	fmt.Fprintf(&b, "Working directory: %s\n", d.WorkingDir)
	if d.IsGitRepo {
		b.WriteString("Is directory a git repo: yes\n")
	} else {
		b.WriteString("Is directory a git repo: no\n")
	}
	fmt.Fprintf(&b, "Platform: %s\n", d.Platform)
	fmt.Fprintf(&b, "Today's date: %s\n", d.Date)
	if d.GitStatus != "" {
		b.WriteString("\nGit status (snapshot at conversation start - may be outdated):\n")
		b.WriteString(d.GitStatus)
		b.WriteString("\n")
	}
	b.WriteString("</env>")
	return Block{Name: "environment", Text: b.String(), Dynamic: true, Scope: ScopeNone}
}

// EnvironmentUpdate renders a replacement notice for an environment change.
// It states explicitly that it replaces earlier values, so the model does
// not merge a stale working directory with the current one.
func EnvironmentUpdate(d PromptDat, changed []string) Block {
	if len(changed) == 0 {
		return Block{}
	}
	var b strings.Builder
	b.WriteString("# Environment update\n")
	b.WriteString("This replaces what this session was told earlier about: ")
	b.WriteString(strings.Join(changed, ", "))
	b.WriteString(".\n\n")
	b.WriteString(EnvironmentBlock(d).Text)
	return Block{Name: "environment-update", Text: b.String(), Dynamic: true, Scope: ScopeNone}
}
