package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateDatabases(t *testing.T) {
	t.Parallel()
	valid := DatabaseConfig{Name: "main", Type: "postgres", URL: "$DATABASE_URL"}
	cfg := &Config{Databases: []DatabaseConfig{valid}}
	require.NoError(t, cfg.ValidateDatabases())
	for _, dbs := range [][]DatabaseConfig{
		{valid, valid}, {{Name: "bad/name", Type: "postgres", URL: "secret"}},
		{{Name: "main", Type: "sqlite", URL: "secret"}}, {{Name: "main", Type: "mysql"}},
	} {
		cfg := &Config{Databases: dbs}
		err := cfg.ValidateDatabases()
		require.Error(t, err)
		require.NotContains(t, err.Error(), "secret")
	}
}
