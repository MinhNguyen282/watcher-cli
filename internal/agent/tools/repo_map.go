package tools

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/repomap"
)

//go:embed repo_map.md
var repoMapToolDescription string

// RepoMapToolName is the name of the repository map query tool.
const RepoMapToolName = "repo_map"

// RepoMapParams describes a repository map query.
type RepoMapParams struct {
	Query  string `json:"query" description:"What to look up: blast_radius, callers, callees, implementers, related, or summary"`
	Target string `json:"target" description:"A file path, symbol name, or symbol ID to look up"`
	Depth  int    `json:"depth" description:"Blast radius depth in hops (default 2, max 5)"`
}

// NewRepoMapTool returns the repository map query tool backed by the
// given service. A nil service yields a tool that reports the map as
// unavailable.
func NewRepoMapTool(svc *repomap.Service) fantasy.AgentTool {
	return fantasy.NewAgentTool(RepoMapToolName, repoMapToolDescription,
		func(ctx context.Context, params RepoMapParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if svc == nil {
				return fantasy.NewTextErrorResponse("The repository map is not available in this directory"), nil
			}
			g, err := svc.Load(ctx)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to load repository map: %v", err)), nil
			}
			if len(g.Files) == 0 {
				return fantasy.NewTextErrorResponse("The repository map is empty (no indexable files found)"), nil
			}

			query := strings.TrimSpace(strings.ToLower(params.Query))
			target := strings.TrimSpace(params.Target)
			depth := params.Depth
			if depth <= 0 {
				depth = 2
			}
			depth = min(depth, 5)

			switch query {
			case "blast_radius", "blast":
				symbols, files := g.BlastRadius([]string{target}, depth)
				return fantasy.NewTextResponse(renderBlastRadius(target, depth, symbols, files)), nil
			case "callers":
				return fantasy.NewTextResponse(renderSymbols("Callers of "+target, g.Callers(target))), nil
			case "callees":
				return fantasy.NewTextResponse(renderSymbols("Callees of "+target, g.Callees(target))), nil
			case "implementers":
				return fantasy.NewTextResponse(renderSymbols("Implementers of "+target, g.Implementers(target))), nil
			case "related":
				return fantasy.NewTextResponse(renderFiles("Files related to "+target, g.RelatedFiles(target))), nil
			case "summary", "":
				return fantasy.NewTextResponse(renderSummary(g)), nil
			default:
				return fantasy.NewTextErrorResponse(fmt.Sprintf("unknown query %q (expected blast_radius, callers, callees, implementers, related, or summary)", params.Query)), nil
			}
		})
}

func renderBlastRadius(target string, depth int, symbols []repomap.Symbol, files []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Blast radius of %q (depth %d):\n", target, depth)
	if len(symbols) > 0 {
		sb.WriteString("\nSymbols:\n")
		for i, s := range symbols {
			if i >= 40 {
				fmt.Fprintf(&sb, "  ... and %d more\n", len(symbols)-i)
				break
			}
			fmt.Fprintf(&sb, "  - %s [%s] %s:%d (%s)\n", s.ID, s.Kind, s.File, s.Line, s.Confidence)
		}
	}
	if len(files) > 0 {
		sb.WriteString("\nFiles:\n")
		for i, f := range files {
			if i >= 40 {
				fmt.Fprintf(&sb, "  ... and %d more\n", len(files)-i)
				break
			}
			fmt.Fprintf(&sb, "  - %s\n", f)
		}
	}
	if len(symbols) == 0 && len(files) == 0 {
		sb.WriteString("\nNothing found. The target may be misspelled; use summary to explore the map.\n")
	}
	return sb.String()
}

func renderSymbols(title string, symbols []repomap.Symbol) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s:\n", title)
	if len(symbols) == 0 {
		sb.WriteString("  (none)\n")
		return sb.String()
	}
	for i, s := range symbols {
		if i >= 40 {
			fmt.Fprintf(&sb, "  ... and %d more\n", len(symbols)-i)
			break
		}
		fmt.Fprintf(&sb, "  - %s [%s] %s:%d\n", s.ID, s.Kind, s.File, s.Line)
	}
	return sb.String()
}

func renderFiles(title string, files []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s:\n", title)
	if len(files) == 0 {
		sb.WriteString("  (none)\n")
		return sb.String()
	}
	for i, f := range files {
		if i >= 40 {
			fmt.Fprintf(&sb, "  ... and %d more\n", len(files)-i)
			break
		}
		fmt.Fprintf(&sb, "  - %s\n", f)
	}
	return sb.String()
}

func renderSummary(g *repomap.Graph) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Repository map summary (%d files, %d symbols):\n",
		len(g.Files), len(g.Symbols))
	top := g.TopSymbols(30)
	for _, s := range top {
		fmt.Fprintf(&sb, "  - %s [%s] %s:%d\n", s.ID, s.Kind, s.File, s.Line)
	}
	return sb.String()
}
