package stdlib

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// FindModulePath finds the nearest go.mod for a given path and returns the module name.
func FindModulePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}

	dir := abs
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		dir = filepath.Dir(abs)
	}

	for {
		modFile := filepath.Join(dir, "go.mod")
		if info, err := os.Stat(modFile); err == nil && !info.IsDir() {
			return parseModName(modFile)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return ""
}

func parseModName(modPath string) string {
	f, err := os.Open(modPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}
