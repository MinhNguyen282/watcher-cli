package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/fantasy"
)

const (
	deepSeekDefaultBaseURL = "https://api.deepseek.com"
	deepSeekMaxSearchCount = 20
	deepSeekMaxFetchBytes  = 5 * 1024 * 1024
)

type deepSeekSearchRequest struct {
	Query       string `json:"query"`
	Count       int    `json:"count"`
	NeedContent bool   `json:"need_content"`
}

type deepSeekSearchResult struct {
	Title     string  `json:"title"`
	URL       string  `json:"url"`
	Snippet   string  `json:"snippet"`
	Summary   string  `json:"summary"`
	Date      string  `json:"date"`
	Rerank    float64 `json:"rerank_score"`
	Authority int     `json:"authority"`
}

type deepSeekSearchResponse struct {
	Results []deepSeekSearchResult `json:"results"`
}

// DeepSeekPage contains the title and Markdown extracted from a page.
type DeepSeekPage struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

type deepSeekAPIError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// FetchDeepSeekPage retrieves a page through DeepSeek's Fetch API.
func FetchDeepSeekPage(ctx context.Context, client *http.Client, baseURL, apiKey, targetURL string) (DeepSeekPage, error) {
	parsedURL, err := url.ParseRequestURI(targetURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" || parsedURL.User != nil || len(targetURL) > 8192 {
		return DeepSeekPage{}, fmt.Errorf("invalid fetch URL")
	}
	if strings.TrimSpace(apiKey) == "" {
		return DeepSeekPage{}, fmt.Errorf("DeepSeek API key is not configured")
	}

	endpoint, err := deepSeekToolsEndpoint(baseURL, "fetch")
	if err != nil {
		return DeepSeekPage{}, err
	}
	var page DeepSeekPage
	if err := deepSeekPost(ctx, client, endpoint, apiKey, map[string]string{"url": targetURL}, deepSeekMaxFetchBytes, &page); err != nil {
		return DeepSeekPage{}, err
	}
	if strings.TrimSpace(page.Content) == "" {
		return DeepSeekPage{}, fmt.Errorf("DeepSeek Fetch returned no page content")
	}
	return page, nil
}

// NewDeepSeekWebSearchTool creates a search tool backed by DeepSeek's Search API.
func NewDeepSeekWebSearchTool(client *http.Client, baseURL, apiKey string) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		WebSearchToolName,
		"Search the web using DeepSeek's Search API. Successful requests may be billed separately from model usage.",
		func(ctx context.Context, params WebSearchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			query := strings.TrimSpace(params.Query)
			if query == "" {
				return fantasy.NewTextErrorResponse("query is required"), nil
			}
			if utf8.RuneCountInString(query) > 128 {
				return fantasy.NewTextErrorResponse("query must be at most 128 characters"), nil
			}
			if strings.TrimSpace(apiKey) == "" {
				return fantasy.NewTextErrorResponse("DeepSeek API key is not configured"), nil
			}

			count := params.MaxResults
			if count <= 0 {
				count = 10
			}
			if count > deepSeekMaxSearchCount {
				count = deepSeekMaxSearchCount
			}
			endpoint, err := deepSeekToolsEndpoint(baseURL, "search")
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			var response deepSeekSearchResponse
			err = deepSeekPost(ctx, client, endpoint, apiKey, deepSeekSearchRequest{
				Query:       query,
				Count:       count,
				NeedContent: false,
			}, 2*1024*1024, &response)
			if err != nil {
				return fantasy.NewTextErrorResponse("Failed to search with DeepSeek: " + err.Error()), nil
			}
			return fantasy.NewTextResponse(formatDeepSeekSearchResults(response.Results)), nil
		},
	)
}

// NewDeepSeekWebFetchTool creates a page-fetch tool backed by DeepSeek's Fetch API.
func NewDeepSeekWebFetchTool(workingDir string, client *http.Client, baseURL, apiKey string) fantasy.AgentTool {
	return fantasy.NewParallelAgentTool(
		WebFetchToolName,
		"Fetch a web page as Markdown using DeepSeek's Fetch API. Successful requests may be billed separately from model usage.",
		func(ctx context.Context, params WebFetchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.URL == "" {
				return fantasy.NewTextErrorResponse("url is required"), nil
			}
			page, err := FetchDeepSeekPage(ctx, client, baseURL, apiKey, params.URL)
			if err != nil {
				return fantasy.NewTextErrorResponse("Failed to fetch URL with DeepSeek: " + err.Error()), nil
			}

			content := strings.TrimSpace(page.Content)
			if page.Title != "" {
				content = "# " + page.Title + "\n\n" + content
			}
			if len(content) <= LargeContentThreshold {
				return fantasy.NewTextResponse(fmt.Sprintf("Fetched content from %s using DeepSeek Fetch:\n\n%s", params.URL, content)), nil
			}

			tempFile, err := os.CreateTemp(workingDir, "page-*.md")
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to create temporary file: %s", err)), nil
			}
			tempFilePath := tempFile.Name()
			if _, err := tempFile.WriteString(content); err != nil {
				_ = tempFile.Close()
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to write content to file: %s", err)), nil
			}
			if err := tempFile.Close(); err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to close temporary file: %s", err)), nil
			}
			return fantasy.NewTextResponse(fmt.Sprintf("Fetched content from %s using DeepSeek Fetch (large page)\n\nContent saved to: %s\n\nUse the view and grep tools to analyze this file.", params.URL, tempFilePath)), nil
		},
	)
}

func deepSeekToolsEndpoint(baseURL, tool string) (string, error) {
	if baseURL == "" {
		baseURL = deepSeekDefaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid DeepSeek API base URL")
	}

	basePath := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(basePath, "/v1") {
		u.Path = basePath + "/tools/" + tool
	} else {
		u.Path = basePath + "/v1/tools/" + tool
	}
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func deepSeekPost(ctx context.Context, client *http.Client, endpoint, apiKey string, body any, maxResponseBytes int64, result any) error {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	bodyData, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode DeepSeek request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyData))
	if err != nil {
		return fmt.Errorf("create DeepSeek request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send DeepSeek request: %w", err)
	}
	defer resp.Body.Close()

	responseData, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read DeepSeek response: %w", err)
	}
	if int64(len(responseData)) > maxResponseBytes {
		return fmt.Errorf("DeepSeek response exceeded the %d byte limit", maxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		var apiErr deepSeekAPIError
		if json.Unmarshal(responseData, &apiErr) == nil && apiErr.Error.Message != "" {
			return fmt.Errorf("DeepSeek API returned HTTP %d: %s", resp.StatusCode, apiErr.Error.Message)
		}
		return fmt.Errorf("DeepSeek API returned HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(responseData, result); err != nil {
		return fmt.Errorf("decode DeepSeek response: %w", err)
	}
	return nil
}

func formatDeepSeekSearchResults(results []deepSeekSearchResult) string {
	if len(results) == 0 {
		return "No results found. Try rephrasing your search."
	}
	var output strings.Builder
	fmt.Fprintf(&output, "Found %d DeepSeek search results:\n\n", len(results))
	for i, result := range results {
		fmt.Fprintf(&output, "%d. %s\n   URL: %s\n", i+1, result.Title, result.URL)
		summary := result.Summary
		if summary == "" {
			summary = result.Snippet
		}
		if summary != "" {
			fmt.Fprintf(&output, "   Summary: %s\n", summary)
		}
		if result.Date != "" {
			fmt.Fprintf(&output, "   Date: %s\n", result.Date)
		}
		fmt.Fprintf(&output, "   Relevance: %.1f/3; authority: %d/4\n", result.Rerank, result.Authority)
		output.WriteByte('\n')
	}
	return output.String()
}
