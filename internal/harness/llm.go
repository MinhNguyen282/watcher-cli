package harness

import (
	"context"
	"fmt"
	"maps"
	"os"
	"strings"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"
	"charm.land/fantasy/providers/openrouter"
	"github.com/charmbracelet/crush/internal/config"
)

// NewChatAgent builds a tool-less fantasy agent bound to the currently
// selected large model in the crush config. It is the LLM client used
// by the harness editor (propose / compose / reflect). Supported
// provider families: anthropic, openai, openai-compatible (including
// custom endpoints such as ollama), google, and openrouter.
func NewChatAgent(ctx context.Context, store *config.ConfigStore, systemPrompt string) (fantasy.Agent, error) {
	cfg := store.Config()
	selected, ok := cfg.Models[config.SelectedModelTypeLarge]
	if !ok {
		return nil, fmt.Errorf("no large model selected; run 'crush' to configure a provider")
	}
	providerCfg, ok := cfg.Providers.Get(selected.Provider)
	if !ok {
		return nil, fmt.Errorf("provider %q is not configured", selected.Provider)
	}
	apiKey, _ := store.Resolve(providerCfg.APIKey)
	baseURL, _ := store.Resolve(providerCfg.BaseURL)

	provider, err := buildEditorProvider(providerCfg, apiKey, baseURL)
	if err != nil {
		return nil, err
	}
	model, err := provider.LanguageModel(ctx, selected.Model)
	if err != nil {
		return nil, fmt.Errorf("failed to load model %q: %w", selected.Model, err)
	}
	return fantasy.NewAgent(model, fantasy.WithSystemPrompt(systemPrompt)), nil
}

// buildEditorProvider constructs a provider the same way the agent
// coordinator does, minus the features the editor does not need.
func buildEditorProvider(providerCfg config.ProviderConfig, apiKey, baseURL string) (fantasy.Provider, error) {
	headers := maps.Clone(providerCfg.ExtraHeaders)
	if headers == nil {
		headers = make(map[string]string)
	}

	switch string(providerCfg.Type) {
	case anthropic.Name:
		var opts []anthropic.Option
		switch {
		case strings.HasPrefix(apiKey, "Bearer "):
			// Prevent the SDK from picking up the API key from env.
			_ = os.Setenv("ANTHROPIC_API_KEY", "")
			headers["Authorization"] = apiKey
		case apiKey != "":
			opts = append(opts, anthropic.WithAPIKey(apiKey))
		}
		if len(headers) > 0 {
			opts = append(opts, anthropic.WithHeaders(headers))
		}
		if baseURL != "" {
			opts = append(opts, anthropic.WithBaseURL(baseURL))
		}
		return anthropic.New(opts...), nil

	case openai.Name:
		opts := []openai.Option{
			openai.WithAPIKey(apiKey),
			openai.WithUseResponsesAPI(),
		}
		if len(headers) > 0 {
			opts = append(opts, openai.WithHeaders(headers))
		}
		if baseURL != "" {
			opts = append(opts, openai.WithBaseURL(baseURL))
		}
		return openai.New(opts...), nil

	case openrouter.Name:
		opts := []openrouter.Option{openrouter.WithAPIKey(apiKey)}
		if len(headers) > 0 {
			opts = append(opts, openrouter.WithHeaders(headers))
		}
		return openrouter.New(opts...), nil

	case google.Name:
		opts := []google.Option{
			google.WithBaseURL(baseURL),
			google.WithGeminiAPIKey(apiKey),
		}
		if len(headers) > 0 {
			opts = append(opts, google.WithHeaders(headers))
		}
		return google.New(opts...), nil

	case openaicompat.Name:
		opts := []openaicompat.Option{
			openaicompat.WithBaseURL(baseURL),
			openaicompat.WithAPIKey(apiKey),
		}
		if len(headers) > 0 {
			opts = append(opts, openaicompat.WithHeaders(headers))
		}
		return openaicompat.New(opts...), nil

	case "azure", "bedrock", "vercel", "google-vertex":
		return nil, fmt.Errorf("the harness editor does not support the %q provider yet", providerCfg.Type)

	default:
		// Known custom providers (ollama, litellm, llamacpp, ...) are
		// openai-compatible under the hood and always carry a base URL.
		if baseURL == "" {
			return nil, fmt.Errorf("provider type %q is not supported by the harness editor", providerCfg.Type)
		}
		opts := []openaicompat.Option{
			openaicompat.WithBaseURL(baseURL),
			openaicompat.WithAPIKey(apiKey),
		}
		if len(headers) > 0 {
			opts = append(opts, openaicompat.WithHeaders(headers))
		}
		return openaicompat.New(opts...), nil
	}
}

// ChatOnce sends a single prompt to the agent and returns the plain
// text response.
func ChatOnce(ctx context.Context, agent fantasy.Agent, userPrompt string) (string, error) {
	maxTokens := int64(16_000)
	result, err := agent.Generate(ctx, fantasy.AgentCall{
		Prompt:          userPrompt,
		MaxOutputTokens: &maxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("editor LLM call failed: %w", err)
	}
	var sb strings.Builder
	for _, c := range result.Response.Content {
		if tc, ok := fantasy.AsContentType[fantasy.TextContent](c); ok {
			sb.WriteString(tc.Text)
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return "", fmt.Errorf("editor LLM returned no text")
	}
	return text, nil
}
