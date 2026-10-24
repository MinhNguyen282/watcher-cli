package config

import (
	"fmt"
	"regexp"
	"strings"
)

var databaseNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// ValidateDatabases checks connection declarations without resolving secrets.
func (c *Config) ValidateDatabases() error {
	seen := make(map[string]bool)
	for _, db := range c.Databases {
		if !databaseNamePattern.MatchString(db.Name) {
			return fmt.Errorf("invalid database name %q", db.Name)
		}
		if seen[db.Name] {
			return fmt.Errorf("duplicate database name %q", db.Name)
		}
		seen[db.Name] = true
		if db.Type != "postgres" && db.Type != "mysql" {
			return fmt.Errorf("database %q must use postgres or mysql", db.Name)
		}
		if strings.TrimSpace(db.URL) == "" {
			return fmt.Errorf("database %q requires a URL", db.Name)
		}
	}
	return nil
}
