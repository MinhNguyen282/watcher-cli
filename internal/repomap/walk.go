package repomap

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/charlievieth/fastwalk"
)

// skipDirs are directories never indexed.
var skipDirs = map[string]bool{
	".git":          true,
	".watcher":      true,
	".hg":           true,
	".svn":          true,
	"node_modules":  true,
	"vendor":        true,
	"dist":          true,
	"build":         true,
	"out":           true,
	"target":        true,
	"bin":           true,
	"obj":           true,
	"coverage":      true,
	".next":         true,
	".venv":         true,
	"venv":          true,
	"__pycache__":   true,
	".idea":         true,
	".vscode":       true,
	".cache":        true,
	".tox":          true,
	".mypy_cache":   true,
	".pytest_cache": true,
	".ruff_cache":   true,
}

// maxIndexedFileSize bounds the files parsed into symbols.
const maxIndexedFileSize = 2 << 20 // 2 MiB

// walkRepo inventories indexable files under root. Git repositories are
// listed through git so the index respects .gitignore; other directories
// fall back to a filesystem walk. Running git also handles a working
// directory nested inside a larger repository.
func walkRepo(root string) ([]FileInfo, error) {
	if files, err := gitRepoFiles(root); err == nil {
		return files, nil
	}
	return fastwalkRepo(root)
}

// gitRepoFiles lists tracked and untracked, non-ignored files.
func gitRepoFiles(root string) ([]FileInfo, error) {
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	files := make([]FileInfo, 0, bytesCount(out))
	seen := make(map[string]bool)
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		rel = filepath.ToSlash(rel)
		if seen[rel] || skipRepoPath(rel) {
			continue
		}
		lang := languageFor(rel)
		if lang == "" {
			continue
		}
		info, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if statErr != nil || info.IsDir() || info.Size() > maxIndexedFileSize {
			continue
		}
		seen[rel] = true
		files = append(files, makeFileInfo(rel, lang, info))
	}
	sortFileInfos(files)
	return files, nil
}

// fastwalkRepo inventories files without git metadata.
func fastwalkRepo(root string) ([]FileInfo, error) {
	var files []FileInfo
	var mu sync.Mutex
	err := fastwalk.Walk(&fastwalk.Config{ToSlash: true, Sort: fastwalk.SortFilesFirst}, root,
		func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // unreadable entries are skipped
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return nil
			}
			if d.IsDir() {
				if rel == "." {
					return nil
				}
				if skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") {
					return fastwalk.SkipDir
				}
				return nil
			}
			rel = filepath.ToSlash(rel)
			lang := languageFor(rel)
			if lang == "" {
				return nil
			}
			info, infoErr := d.Info()
			if infoErr != nil || info.Size() > maxIndexedFileSize {
				return nil
			}
			mu.Lock()
			files = append(files, makeFileInfo(rel, lang, info))
			mu.Unlock()
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("failed to walk repository: %w", err)
	}
	sortFileInfos(files)
	return files, nil
}

// skipRepoPath reports whether a repository-relative path belongs to a
// directory the index never reads.
func skipRepoPath(rel string) bool {
	for _, segment := range strings.Split(rel, "/") {
		if segment == "" {
			continue
		}
		if skipDirs[segment] || strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

func makeFileInfo(rel, lang string, info os.FileInfo) FileInfo {
	return FileInfo{
		Path:     rel,
		Language: lang,
		HasTests: IsTestFile(rel),
		Size:     info.Size(),
		Stamp:    fmt.Sprintf("%d:%d", info.ModTime().Unix(), info.Size()),
	}
}

func sortFileInfos(files []FileInfo) {
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
}

func bytesCount(b []byte) int {
	n := 0
	for _, c := range b {
		if c == 0 {
			n++
		}
	}
	return n
}

// readFile reads a repository file, capped at maxIndexedFileSize.
func readRepoFile(root, path string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return nil, err
	}
	if len(data) > maxIndexedFileSize {
		return data[:maxIndexedFileSize], nil
	}
	return data, nil
}
