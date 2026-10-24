package dbinspector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadOpenAPIVersions(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"openapi", "swagger"} {
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			spec := `openapi: 3.1.0
info: {title: Test API, version: v1}
paths:
  /users:
    post:
      requestBody:
        content:
          application/json:
            schema: {$ref: '#/components/schemas/User'}
      responses:
        202:
          content:
            application/json:
              schema: {$ref: '#/components/schemas/User'}
    get:
      summary: List users
components:
  schemas:
    User: {type: object, properties: {name: {type: string}}}
`
			if version == "swagger" {
				spec = `swagger: '2.0'
info: {title: Test API, version: v1}
paths:
  /users:
    post:
      parameters:
        - in: body
          name: user
          schema: {$ref: '#/definitions/User'}
      responses:
        202:
          schema: {$ref: '#/definitions/User'}
    get:
      summary: List users
definitions:
  User: {type: object, properties: {name: {type: string}}}
`
			}
			path := filepath.Join(t.TempDir(), "spec.yaml")
			require.NoError(t, os.WriteFile(path, []byte(spec), 0o600))
			got, err := LoadOpenAPI(path)
			require.NoError(t, err)
			require.Equal(t, "Test API", got.Title)
			require.Len(t, got.Endpoints, 2)
			require.Equal(t, "GET", got.Endpoints[0].Method)
			require.Contains(t, got.Endpoints[1].RequestSchema, "name:")
			require.Contains(t, got.Endpoints[1].ResponseSchema, "schema User:")
			for range 5 {
				again, err := LoadOpenAPI(path)
				require.NoError(t, err)
				require.Equal(t, got, again)
			}
		})
	}
}

func TestLoadOpenAPIRejectsInvalidAndOversizedFiles(t *testing.T) {
	t.Parallel()
	for _, spec := range []string{"not: an OpenAPI spec", "[invalid", strings.Repeat("x", (2<<20)+1)} {
		path := filepath.Join(t.TempDir(), "spec.yaml")
		require.NoError(t, os.WriteFile(path, []byte(spec), 0o600))
		_, err := LoadOpenAPI(path)
		require.Error(t, err)
	}
}
