// Package verify discovers project checks, selects the narrowest useful
// target set, runs the checks, and parses failures for the agent.
package verify

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrNoProfile means no supported project manifest was found.
var ErrNoProfile = errors.New("no supported project manifest found")

// Kind identifies the project family used for command discovery.
type Kind string

const (
	KindGo     Kind = "go"
	KindNode   Kind = "node"
	KindRust   Kind = "rust"
	KindPython Kind = "python"
	KindMake   Kind = "make"
)

// Check is one command that verifies part of the project.
type Check struct {
	Name string
	Argv []string
	Dir  string
}

// Profile is the detected verification plan for one repository.
type Profile struct {
	Root    string
	Kind    Kind
	Manager string
	Checks  []Check
}

// Detect chooses a project profile from the manifests in root. It
// prefers Go, then Node, Rust, Python, and Make.
func Detect(root string) (Profile, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Profile{}, fmt.Errorf("failed to resolve working directory: %w", err)
	}
	if fileExists(filepath.Join(root, "go.mod")) {
		return Profile{
			Root:    root,
			Kind:    KindGo,
			Manager: "go",
			Checks:  []Check{{Name: "test", Argv: []string{"go", "test", "./..."}}},
		}, nil
	}
	var manifestErr error
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		if profile, err := detectNode(root, data); err == nil {
			return profile, nil
		} else if !errors.Is(err, ErrNoProfile) {
			manifestErr = err
		}
	}
	if fileExists(filepath.Join(root, "Cargo.toml")) {
		return Profile{
			Root:    root,
			Kind:    KindRust,
			Manager: "cargo",
			Checks:  []Check{{Name: "test", Argv: []string{"cargo", "test"}}},
		}, nil
	}
	if profile, ok := detectPython(root); ok {
		return profile, nil
	}
	if profile, ok := detectMake(root); ok {
		return profile, nil
	}
	if manifestErr != nil {
		return Profile{}, manifestErr
	}
	return Profile{}, ErrNoProfile
}

type packageJSON struct {
	Scripts        map[string]string `json:"scripts"`
	PackageManager string            `json:"packageManager"`
}

func detectNode(root string, data []byte) (Profile, error) {
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return Profile{}, fmt.Errorf("failed to parse package.json: %w", err)
	}
	manager := resolveNodeManager(root, pkg.PackageManager)

	var checks []Check
	add := func(name string) {
		script, ok := pkg.Scripts[name]
		if !ok || strings.TrimSpace(script) == "" {
			return
		}
		if name == "test" && isPlaceholderTest(script) {
			return
		}
		checks = append(checks, Check{Name: name, Argv: nodeScriptArgv(manager, name)})
	}

	add("test")
	add("typecheck")
	add("type-check")
	if len(checks) == 0 {
		for _, name := range []string{"check", "lint", "build"} {
			add(name)
			if len(checks) > 0 {
				break
			}
		}
	}
	if len(checks) == 0 {
		return Profile{}, ErrNoProfile
	}
	return Profile{Root: root, Kind: KindNode, Manager: manager, Checks: checks}, nil
}

func resolveNodeManager(root, declared string) string {
	var candidates []string
	if declared != "" {
		candidates = append(candidates, strings.SplitN(declared, "@", 2)[0])
	}
	switch {
	case fileExists(filepath.Join(root, "pnpm-lock.yaml")):
		candidates = append(candidates, "pnpm")
	case fileExists(filepath.Join(root, "yarn.lock")):
		candidates = append(candidates, "yarn")
	case fileExists(filepath.Join(root, "bun.lockb")), fileExists(filepath.Join(root, "bun.lock")):
		candidates = append(candidates, "bun")
	}
	candidates = append(candidates, "npm")
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate
		}
	}
	return "npm"
}

func nodeScriptArgv(manager, name string) []string {
	switch manager {
	case "pnpm":
		return []string{"pnpm", "run", name}
	case "yarn":
		return []string{"yarn", name}
	case "bun":
		return []string{"bun", "run", name}
	default:
		if name == "test" {
			return []string{"npm", "test"}
		}
		return []string{"npm", "run", name}
	}
}

func isPlaceholderTest(script string) bool {
	return strings.Contains(strings.ToLower(script), "no test specified")
}

func detectPython(root string) (Profile, bool) {
	hasPytest := false
	if data, err := os.ReadFile(filepath.Join(root, "pyproject.toml")); err == nil {
		hasPytest = strings.Contains(string(data), "pytest")
	}
	if !hasPytest {
		if data, err := os.ReadFile(filepath.Join(root, "setup.cfg")); err == nil {
			hasPytest = strings.Contains(string(data), "[tool:pytest]") || strings.Contains(string(data), "pytest")
		}
	}
	if !hasPytest {
		if data, err := os.ReadFile(filepath.Join(root, "tox.ini")); err == nil {
			hasPytest = strings.Contains(string(data), "pytest") || strings.Contains(string(data), "[testenv]")
		}
	}
	hasPytest = hasPytest || fileExists(filepath.Join(root, "pytest.ini"))
	if !hasPytest {
		return Profile{}, false
	}

	python := ""
	for _, candidate := range []string{"python", "python3", "py"} {
		if _, err := exec.LookPath(candidate); err == nil {
			python = candidate
			break
		}
	}
	if python == "" {
		return Profile{}, false
	}
	return Profile{
		Root:    root,
		Kind:    KindPython,
		Manager: python,
		Checks:  []Check{{Name: "test", Argv: []string{python, "-m", "pytest", "-q"}}},
	}, true
}

var makeTestTarget = regexp.MustCompile(`(?m)^test\s*:`)

func detectMake(root string) (Profile, bool) {
	for _, name := range []string{"Makefile", "makefile", "GNUmakefile"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		if makeTestTarget.Match(data) {
			return Profile{
				Root:    root,
				Kind:    KindMake,
				Manager: "make",
				Checks:  []Check{{Name: "test", Argv: []string{"make", "test"}}},
			}, true
		}
	}
	return Profile{}, false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
