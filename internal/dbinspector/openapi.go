package dbinspector

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// EndpointSummary describes one OpenAPI operation with its schemas.
type EndpointSummary struct {
	Method         string `json:"method"`
	Path           string `json:"path"`
	Summary        string `json:"summary"`
	RequestSchema  string `json:"request_schema,omitempty"`
	ResponseSchema string `json:"response_schema,omitempty"`
}

// OpenAPISummary is the distilled view of a spec file.
type OpenAPISummary struct {
	Title     string            `json:"title"`
	Version   string            `json:"version"`
	Endpoints []EndpointSummary `json:"endpoints"`
}

// LoadOpenAPI parses an OpenAPI/Swagger JSON or YAML file and returns
// its distilled summary.
func LoadOpenAPI(path string) (OpenAPISummary, error) {
	file, err := os.Open(path)
	if err != nil {
		return OpenAPISummary{}, fmt.Errorf("failed to read spec: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return OpenAPISummary{}, fmt.Errorf("spec must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil {
		return OpenAPISummary{}, fmt.Errorf("failed to read spec: %w", err)
	}
	if len(data) > 2<<20 {
		return OpenAPISummary{}, fmt.Errorf("spec exceeds 2 MiB")
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return OpenAPISummary{}, fmt.Errorf("failed to parse spec: %w", err)
	}
	doc, _ = normalizeYAML(doc).(map[string]any)
	version, _ := doc["openapi"].(string)
	swagger, _ := doc["swagger"].(string)
	if !strings.HasPrefix(version, "3.") && swagger != "2.0" {
		return OpenAPISummary{}, fmt.Errorf("expected OpenAPI 3.x or Swagger 2.0")
	}

	var summary OpenAPISummary
	if info, ok := doc["info"].(map[string]any); ok {
		summary.Title, _ = info["title"].(string)
		summary.Version, _ = info["version"].(string)
	}

	// Reusable component schemas, referenced by endpoint summaries.
	components := extractSchemas(doc)

	paths, _ := doc["paths"].(map[string]any)
	var pathsOrdered []string
	for p := range paths {
		pathsOrdered = append(pathsOrdered, p)
	}
	sort.Strings(pathsOrdered)

	for _, path := range pathsOrdered {
		ops, _ := paths[path].(map[string]any)
		for _, method := range sortedKeys(ops) {
			raw := ops[method]
			if !isHTTPMethod(method) {
				continue
			}
			op, _ := raw.(map[string]any)
			ep := EndpointSummary{Method: strings.ToUpper(method), Path: path}
			if summary, ok := op["summary"].(string); ok {
				ep.Summary = summary
			}
			ep.RequestSchema, ep.ResponseSchema = endpointSchemas(op, components)
			if swagger == "2.0" {
				if params, ok := op["parameters"].([]any); ok {
					for _, raw := range params {
						param, _ := raw.(map[string]any)
						if param["in"] == "body" {
							ep.RequestSchema = resolveSchema(param["schema"], components)
						}
					}
				}
				if responses, ok := op["responses"].(map[string]any); ok {
					for _, code := range sortedKeys(responses) {
						if !strings.HasPrefix(code, "2") && code != "default" {
							continue
						}
						r, _ := responses[code].(map[string]any)
						if schema := resolveSchema(r["schema"], components); schema != "" {
							ep.ResponseSchema = schema
							break
						}
					}
				}
			}
			summary.Endpoints = append(summary.Endpoints, ep)
			if len(summary.Endpoints) > 500 {
				return OpenAPISummary{}, fmt.Errorf("spec exceeds 500 operations")
			}
		}
	}
	return summary, nil
}

// isHTTPMethod reports whether a path item key is an operation.
func isHTTPMethod(key string) bool {
	switch key {
	case "get", "post", "put", "patch", "delete", "options", "head", "trace":
		return true
	}
	return false
}

// extractSchemas returns the named component schemas as compact YAML
// snippets.
func extractSchemas(doc map[string]any) map[string]string {
	out := make(map[string]string)
	if definitions, ok := doc["definitions"].(map[string]any); ok {
		for name, raw := range definitions {
			out[name] = compactYAML(raw)
		}
	}
	components, ok := doc["components"].(map[string]any)
	if !ok {
		return out
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		return out
	}
	for name, raw := range schemas {
		out[name] = compactYAML(raw)
	}
	return out
}

// compactYAML renders a schema value as a short YAML snippet.
func compactYAML(v any) string {
	data, err := yaml.Marshal(v)
	if err != nil {
		return ""
	}
	text := string(data)
	if len(text) > 600 {
		text = text[:600] + "\n... (truncated)"
	}
	return text
}

// endpointSchemas extracts request/response schema references for one
// operation.
func endpointSchemas(op map[string]any, components map[string]string) (request, response string) {
	// Request body.
	if body, ok := op["requestBody"].(map[string]any); ok {
		if content, ok := body["content"].(map[string]any); ok {
			request = schemaRefFromContent(content, components)
		}
	}
	// Responses.
	if responses, ok := op["responses"].(map[string]any); ok {
		for _, code := range sortedKeys(responses) {
			if !strings.HasPrefix(code, "2") && code != "default" {
				continue
			}
			if r, ok := responses[code].(map[string]any); ok {
				if content, ok := r["content"].(map[string]any); ok {
					if s := schemaRefFromContent(content, components); s != "" {
						response = s
						break
					}
				}
			}
		}
	}
	return request, response
}

// schemaRefFromContent resolves the first media type's schema ref or
// inline schema.
func schemaRefFromContent(content map[string]any, components map[string]string) string {
	for _, mediaType := range sortedKeys(content) {
		media := content[mediaType]
		mt, _ := media.(map[string]any)
		schema, ok := mt["schema"].(map[string]any)
		if !ok {
			continue
		}
		return resolveSchema(schema, components)
	}
	return ""
}

// RenderSpec renders the summary as markdown for the agent.
func RenderSpec(s OpenAPISummary) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## %s %s\n", s.Title, s.Version)
	for _, ep := range s.Endpoints {
		fmt.Fprintf(&sb, "- **%s %s** — %s\n", ep.Method, ep.Path, ep.Summary)
		if ep.RequestSchema != "" {
			sb.WriteString("  request: " + strings.ReplaceAll(ep.RequestSchema, "\n", "\n  ") + "\n")
		}
		if ep.ResponseSchema != "" {
			sb.WriteString("  response: " + strings.ReplaceAll(ep.ResponseSchema, "\n", "\n  ") + "\n")
		}
		if sb.Len() > 128<<10 {
			sb.WriteString("\n[Output truncated.]\n")
			break
		}
	}
	if len(s.Endpoints) == 0 {
		return "(no endpoints found)"
	}
	return sb.String()
}

func resolveSchema(raw any, components map[string]string) string {
	schema, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	if ref, ok := schema["$ref"].(string); ok {
		name := strings.TrimPrefix(strings.TrimPrefix(ref, "#/components/schemas/"), "#/definitions/")
		if value, ok := components[name]; ok {
			return "schema " + name + ":\n" + value
		}
		return ref
	}
	return compactYAML(schema)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// normalizeYAML makes numeric response codes work like their JSON equivalents.
func normalizeYAML(value any) any {
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			value[key] = normalizeYAML(item)
		}
		return value
	case map[any]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			result[fmt.Sprint(key)] = normalizeYAML(item)
		}
		return result
	case []any:
		for i, item := range value {
			value[i] = normalizeYAML(item)
		}
		return value
	default:
		return value
	}
}
