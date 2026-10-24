package verify

import (
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/crush/internal/repomap"
)

// Select narrows the profile to the requested or changed targets. A
// profile with no changed files keeps its full-project checks.
func Select(profile Profile, graph *repomap.Graph, requested []string, full bool) (Profile, error) {
	if full {
		return profile, nil
	}
	paths, err := resolveTargets(profile.Root, requested)
	if err != nil {
		return profile, err
	}
	if len(paths) == 0 {
		paths = ChangedFiles(profile.Root)
	}
	if len(paths) == 0 {
		return profile, nil
	}

	switch profile.Kind {
	case KindGo:
		return selectGo(profile, graph, paths), nil
	case KindRust:
		return selectRust(profile, paths), nil
	case KindPython:
		return selectPython(profile, graph, paths), nil
	default:
		return profile, nil
	}
}

// ChangedFiles returns working-tree paths that differ from HEAD,
// including untracked files. It returns nil outside git.
func ChangedFiles(root string) []string {
	cmd := exec.Command("git", "status", "--porcelain", "-z", "--untracked-files=all")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parsePorcelain(out)
}

func parsePorcelain(out []byte) []string {
	entries := strings.Split(string(out), "\x00")
	var changed []string
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		status := entry[:2]
		name := entry[3:]
		if status[0] == 'R' || status[0] == 'C' {
			// With -z the working-tree path comes first and the
			// original path follows. Keep the first, skip the second.
			if i+1 >= len(entries) {
				continue
			}
			i++
		}
		if name == "" {
			continue
		}
		name = strings.TrimPrefix(filepath.ToSlash(name), "./")
		if strings.HasPrefix(name, ".watcher/") || strings.HasPrefix(name, ".crush/") {
			continue
		}
		changed = append(changed, name)
	}
	return changed
}

func resolveTargets(root string, requested []string) ([]string, error) {
	var out []string
	for _, target := range requested {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		absolute := target
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(root, target)
		}
		absolute, err := filepath.Abs(absolute)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve target %q: %w", target, err)
		}
		rel, err := filepath.Rel(root, absolute)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve target %q: %w", target, err)
		}
		rel = filepath.ToSlash(rel)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			return nil, fmt.Errorf("target %q is outside the repository", target)
		}
		out = append(out, strings.TrimPrefix(rel, "./"))
	}
	return out, nil
}

func selectGo(profile Profile, graph *repomap.Graph, paths []string) Profile {
	dirs := make(map[string]bool)
	full := false
	for _, p := range paths {
		if !strings.HasSuffix(strings.ToLower(p), ".go") {
			continue
		}
		dir := path.Dir(p)
		if dir == "." || dir == "" {
			full = true
			continue
		}
		if skippedDir(dir) {
			continue
		}
		dirs[dir] = true
	}
	if graph != nil {
		for _, test := range graph.TestsFor(paths) {
			if !strings.HasSuffix(strings.ToLower(test), ".go") {
				continue
			}
			dir := path.Dir(test)
			if dir == "." || dir == "" {
				full = true
				continue
			}
			dirs[dir] = true
		}
	}

	if full || len(dirs) == 0 || len(dirs) > 12 {
		return profile
	}
	sorted := make([]string, 0, len(dirs))
	for dir := range dirs {
		sorted = append(sorted, dir)
	}
	sort.Strings(sorted)

	argv := []string{"go", "test"}
	for _, dir := range sorted {
		argv = append(argv, "./"+dir)
	}
	profile.Checks = []Check{{Name: "test", Argv: argv}}
	return profile
}

func selectRust(profile Profile, paths []string) Profile {
	manifests := make(map[string]bool)
	for _, p := range paths {
		if !strings.HasSuffix(strings.ToLower(p), ".rs") {
			continue
		}
		dir := path.Dir(p)
		found := ""
		for dir != "." && dir != "" {
			if fileExists(filepath.Join(profile.Root, filepath.FromSlash(dir), "Cargo.toml")) {
				found = dir
				break
			}
			dir = path.Dir(dir)
		}
		if found == "" {
			found = "."
		}
		manifests[found] = true
	}
	if len(manifests) == 0 {
		return profile
	}
	if len(manifests) == 1 && manifests["."] {
		return profile
	}

	sorted := make([]string, 0, len(manifests))
	for dir := range manifests {
		sorted = append(sorted, dir)
	}
	sort.Strings(sorted)

	var checks []Check
	for _, dir := range sorted {
		if dir == "." {
			checks = append(checks, Check{Name: "test", Argv: []string{"cargo", "test"}})
			continue
		}
		checks = append(checks, Check{
			Name: "test",
			Argv: []string{"cargo", "test", "--manifest-path", path.Join(dir, "Cargo.toml")},
		})
	}
	profile.Checks = checks
	return profile
}

func selectPython(profile Profile, graph *repomap.Graph, paths []string) Profile {
	if len(profile.Checks) == 0 {
		return profile
	}
	var targets []string
	for _, p := range paths {
		if strings.HasSuffix(strings.ToLower(p), ".py") && repomap.IsTestFile(p) {
			targets = append(targets, p)
		}
	}
	if len(targets) == 0 && graph != nil {
		for _, test := range graph.TestsFor(paths) {
			if strings.HasSuffix(strings.ToLower(test), ".py") {
				targets = append(targets, test)
			}
		}
	}
	if len(targets) == 0 {
		return profile
	}

	argv := append([]string(nil), profile.Checks[0].Argv...)
	argv = append(argv, targets...)
	profile.Checks = []Check{{Name: "test", Argv: argv}}
	return profile
}

func skippedDir(dir string) bool {
	for _, segment := range strings.Split(dir, "/") {
		if strings.HasPrefix(segment, ".") {
			return true
		}
		switch segment {
		case "vendor", "node_modules", "testdata", "dist", "build", "target":
			return true
		}
	}
	return false
}
