package dbinspector

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Guard bounds read queries. Database read-only transactions are also required.
type Guard struct {
	MaxRows int
	Timeout time.Duration
}

// DefaultGuard caps each query at 200 rows and five seconds.
func DefaultGuard() Guard { return Guard{MaxRows: 200, Timeout: 5 * time.Second} }

// Validate conservatively accepts a single read statement, not arbitrary SQL.
func (g Guard) Validate(query string) error {
	if g.MaxRows <= 0 || g.Timeout <= 0 {
		return fmt.Errorf("query limits must be positive")
	}
	if len(query) > 64<<10 {
		return fmt.Errorf("query exceeds 64 KiB")
	}
	tokens, err := sqlTokens(query)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		return fmt.Errorf("query is empty")
	}
	switch tokens[0] {
	case "SELECT", "WITH", "SHOW", "DESCRIBE", "DESC", "EXPLAIN":
	default:
		return fmt.Errorf("only SELECT, WITH, SHOW, DESCRIBE and EXPLAIN are supported")
	}
	for _, token := range tokens {
		switch token {
		case "INSERT", "UPDATE", "DELETE", "MERGE", "REPLACE", "UPSERT", "CREATE", "ALTER", "DROP", "TRUNCATE",
			"GRANT", "REVOKE", "COPY", "CALL", "DO", "EXEC", "EXECUTE", "SET", "RESET", "INTO", "OUTFILE", "DUMPFILE",
			"LOAD", "LOCK", "UNLOCK", "FOR", "ANALYZE", "ANALYSE", "COMMIT", "ROLLBACK", "BEGIN", "START",
			"VACUUM", "REINDEX", "REFRESH", "ATTACH", "DETACH", "INSTALL", "UNINSTALL",
			"PG_SLEEP", "SLEEP", "BENCHMARK", "PG_ADVISORY_LOCK", "PG_TRY_ADVISORY_LOCK", "GET_LOCK", "RELEASE_LOCK",
			"SET_CONFIG", "NEXTVAL", "SETVAL", "LO_IMPORT", "LO_EXPORT", "DBLINK_EXEC":
			return fmt.Errorf("keyword or function %s is not allowed", token)
		}
	}
	return nil
}

// sqlTokens skips quoted literals and comments, rejecting ambiguous constructs.
func sqlTokens(query string) ([]string, error) {
	var tokens []string
	ended := false
	for i := 0; i < len(query); {
		c := query[i]
		if unicode.IsSpace(rune(c)) {
			i++
			continue
		}
		if c == '#' {
			return nil, fmt.Errorf("hash comments and operators are not supported")
		}
		if c == '-' && i+1 < len(query) && query[i+1] == '-' {
			for i < len(query) && query[i] != '\n' {
				i++
			}
			continue
		}
		if c == '/' && i+1 < len(query) && query[i+1] == '*' {
			if i+2 < len(query) && (query[i+2] == '!' || query[i+2] == 'M') {
				return nil, fmt.Errorf("executable comments are not allowed")
			}
			end := strings.Index(query[i+2:], "*/")
			if end < 0 || strings.Contains(query[i+2:i+2+end], "/*") {
				return nil, fmt.Errorf("unterminated or nested comment")
			}
			i += end + 4
			continue
		}
		if ended {
			return nil, fmt.Errorf("multiple statements are not allowed")
		}
		if c == ';' {
			ended = true
			i++
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			start := i
			quote := c
			i++
			closed := false
			for i < len(query) {
				if query[i] == '\\' {
					return nil, fmt.Errorf("backslash escapes are not supported")
				}
				if query[i] == quote {
					i++
					if i < len(query) && query[i] == quote {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated quoted value")
			}
			if quote != '\'' {
				tokens = append(tokens, strings.ToUpper(query[start+1:i-1]))
			}
			continue
		}
		if c == '$' || c == '@' || c == '\\' {
			return nil, fmt.Errorf("dollar quoting, variables and escapes are not supported")
		}
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' {
			start := i
			i++
			for i < len(query) && (query[i] >= 'a' && query[i] <= 'z' || query[i] >= 'A' && query[i] <= 'Z' || query[i] >= '0' && query[i] <= '9' || query[i] == '_') {
				i++
			}
			tokens = append(tokens, strings.ToUpper(query[start:i]))
			continue
		}
		i++
	}
	return tokens, nil
}
