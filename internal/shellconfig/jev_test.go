package shellconfig

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJevOptions(t *testing.T) {
	t.Parallel()
	data, err := LoadShellConfig(t.Context(), filepath.Join(t.TempDir(), "crushrc"), []byte("option jev-observe true\noption jev-model jev-test\n"))
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))
	options := result["options"].(map[string]any)
	require.Equal(t, true, options["jev_observe"])
	require.Equal(t, "jev-test", options["jev_model"])
}
