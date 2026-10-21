package repomap

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

// walkRepo inventories indexable files under root.
func walkRepo(root string) ([]FileInfo, error) {
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
			lang := languageFor(rel)
			if lang == "" {
				return nil
			}
			info, infoErr := d.Info()
			if infoErr != nil || info.Size() > maxIndexedFileSize {
				return nil
			}
			mu.Lock()
			files = append(files, FileInfo{
				Path:     filepath.ToSlash(rel),
				Language: lang,
				Size:     info.Size(),
				Stamp:    fmt.Sprintf("%d:%d", info.ModTime().Unix(), info.Size()),
			})
			mu.Unlock()
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("failed to walk repository: %w", err)
	}
	return files, nil
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
