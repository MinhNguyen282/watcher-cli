package shellconfig

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDatabaseBuiltin(t *testing.T) {
	t.Parallel()
	data, err := LoadShellConfig(t.Context(), filepath.Join(t.TempDir(), "crushrc"), []byte(`
db add main --type postgres --url '$DATABASE_URL'
db add main --type mysql --url '$MYSQL_DSN'
db add unused --type postgres --url '$OTHER_URL'
db remove unused
db list
`))
	require.NoError(t, err)
	var cfg struct {
		Databases []struct{ Name, Type, URL string }
	}
	require.NoError(t, json.Unmarshal(data, &cfg))
	require.Len(t, cfg.Databases, 1)
	require.Equal(t, "main", cfg.Databases[0].Name)
	require.Equal(t, "mysql", cfg.Databases[0].Type)
	require.Equal(t, "$MYSQL_DSN", cfg.Databases[0].URL)
}

func TestDatabaseBuiltinRejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	for _, script := range []string{"db", "db add main --type postgres", "db add main --url x", "db add main --type sqlite --url x", "db add ../bad --type postgres --url x", "db remove main extra", "db list extra", "db add main --type mysql --url x --unknown y"} {
		t.Run(script, func(t *testing.T) {
			t.Parallel()
			_, err := LoadShellConfig(t.Context(), filepath.Join(t.TempDir(), "crushrc"), []byte(script))
			require.Error(t, err)
		})
	}
}
