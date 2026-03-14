package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// ignoreMatcher handles .gomopignore pattern matching.
type ignoreMatcher struct {
	patterns []string
	rootDir  string
}

// newIgnoreMatcher creates a matcher by loading patterns from .gomopignore files.
// It looks for .gomopignore in the given directory and its parents up to the git root.
func newIgnoreMatcher(dir string) *ignoreMatcher {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}

	im := &ignoreMatcher{
		rootDir: absDir,
	}

	// Load patterns from .gomopignore in the directory
	im.loadPatterns(absDir)

	return im
}

// loadPatterns reads .gomopignore from the given directory.
func (im *ignoreMatcher) loadPatterns(dir string) {
	ignoreFile := filepath.Join(dir, ".gomopignore")
	f, err := os.Open(ignoreFile)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		im.patterns = append(im.patterns, line)
	}
}

// match checks if a path should be ignored.
// The path should be relative to the root directory.
func (im *ignoreMatcher) match(path string) bool {
	if len(im.patterns) == 0 {
		return false
	}

	// Get relative path from root
	relPath := path
	if filepath.IsAbs(path) {
		var err error
		relPath, err = filepath.Rel(im.rootDir, path)
		if err != nil {
			return false
		}
	}

	// Normalize path separators
	relPath = filepath.ToSlash(relPath)

	for _, pattern := range im.patterns {
		// Handle directory patterns (ending with /)
		isDirPattern := strings.HasSuffix(pattern, "/")
		if isDirPattern {
			pattern = strings.TrimSuffix(pattern, "/")
		}

		// Normalize pattern
		pattern = filepath.ToSlash(pattern)

		// Match the pattern
		matched, err := doublestar.Match(pattern, relPath)
		if err != nil {
			continue
		}
		if matched {
			return true
		}

		// Also try matching as if pattern has ** prefix (for relative patterns)
		if !strings.HasPrefix(pattern, "/") && !strings.HasPrefix(pattern, "**") {
			matched, _ = doublestar.Match("**/"+pattern, relPath)
			if matched {
				return true
			}
		}

		// For directory patterns, also match the path with trailing slash
		if isDirPattern {
			matched, _ = doublestar.Match(pattern, relPath+"/")
			if matched {
				return true
			}
		}
	}

	return false
}