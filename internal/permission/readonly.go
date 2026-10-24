package permission

import (
	"strings"
	"unicode"
)

// readOnlyCommands is the allowlist for shell commands that cannot change
// repository or machine state. It is intentionally small: every entry is a
// command whose default invocation only reads. Anything not listed, or a
// listed command with a write flag, still asks the user.
var readOnlyCommands = map[string]bool{
	"cat":    true,
	"cd":     true,
	"du":     true,
	"echo":   true,
	"file":   true,
	"git":    true, // limited by readOnlyGitSubcommands below
	"head":   true,
	"ls":     true,
	"pwd":    true,
	"rg":     true,
	"sort":   true,
	"stat":   true,
	"tail":   true,
	"tree":   true,
	"uniq":   true,
	"wc":     true,
	"which":  true,
	"whoami": true,
}

// readOnlyGitSubcommands are the git subcommands that only read. Commands
// that take a write flag (branch -D, tag -d, remote add, config --unset)
// are excluded from auto-approval by the flag check below.
var readOnlyGitSubcommands = map[string]bool{
	"blame":        true,
	"branch":       true,
	"cat-file":     true,
	"describe":     true,
	"diff":         true,
	"for-each-ref": true,
	"log":          true,
	"ls-files":     true,
	"rev-parse":    true,
	"show":         true,
	"status":       true,
	"tag":          true,
	"whatchanged":  true,
}

// readOnlyGitWriteFlags are the flags that turn a read-looking git
// subcommand into a write. Presence of any of them drops the command out of
// the allowlist.
var readOnlyGitWriteFlags = map[string]bool{
	"-d": true, "--delete": true, "-D": true,
	"-m": true, "-M": true, "-c": true, "-C": true,
	"-f": true, "--force": true,
	"--set-upstream": true, "--unset": true,
	"-u": true, "--add": true, "--remove": true,
	"--hard": true, "--soft": true, "--mixed": true,
	"-p": true, "--prune": true,
}

// shellMetacharacters are the characters that compose commands. A command
// that contains one is never auto-approved: the allowlist only answers for a
// single plain command, never for a pipeline or a redirect.
const shellMetacharacters = "|&;<>`$()\n\r\\"

// IsReadOnlyCommand reports whether a shell command is safe to run without
// asking: a single plain command whose program is allowlisted, with no
// shell metacharacters and no write flag on a git subcommand.
func IsReadOnlyCommand(command string) bool {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, shellMetacharacters) {
		return false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return false
	}
	program := baseCommand(fields[0])
	if !readOnlyCommands[program] {
		return false
	}
	if program == "git" {
		return isReadOnlyGitCommand(fields[1:])
	}
	// A leading path in front of an allowlisted program is not enough:
	// "./cat" may be an arbitrary executable.
	if strings.ContainsAny(fields[0], "/\\") {
		return false
	}
	return true
}

func isReadOnlyGitCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	subcommand := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			// Global git flags such as -C can change the repository or run
			// a command; refuse them for auto-approval.
			return false
		}
		subcommand = arg
		break
	}
	if !readOnlyGitSubcommands[subcommand] {
		return false
	}
	for _, arg := range args[1:] {
		if readOnlyGitWriteFlags[arg] {
			return false
		}
	}
	return true
}

func baseCommand(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if idx := strings.LastIndexAny(token, "/\\"); idx >= 0 {
		token = token[idx+1:]
	}
	token = strings.TrimSuffix(token, ".exe")
	return strings.ToLower(token)
}

// LooksLikeWriteCommand reports whether a command contains an
// obvious write intent. It is used for messaging only; the deny decision is
// made by the allowlist above.
func LooksLikeWriteCommand(command string) bool {
	for _, r := range command {
		if unicode.IsControl(r) && r != '\t' {
			return true
		}
	}
	lowered := strings.ToLower(command)
	for _, marker := range []string{">", "rm ", "mv ", "cp ", "chmod ", "chown ", "kill ", "dd "} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}
