package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/alecthomas/kong"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/karrick/godirwalk"
	"github.com/nijaru/gomop/internal/format"
	"github.com/nijaru/gomop/internal/stdlib"
)

var (
	// Version is set by goreleaser
	version = "dev"
)

type CLI struct {
	Write         bool     `help:"write result to (source) file instead of stdout" short:"w"`
	List          bool     `help:"list files whose formatting differs from gomop's" short:"l"`
	Diff          bool     `help:"display diffs instead of rewriting files" short:"d"`
	LineLength    int      `help:"maximum line length" short:"m" default:"100"`
	TabWidth      int      `help:"tab width" short:"t" default:"4"`
	GoVersion     string   `help:"Go version for formatting (e.g., go1.24)" name:"go"`
	ModulePath    string   `help:"module path for import grouping" name:"modpath"`
	LocalPrefixes []string `help:"comma-separated local import prefixes" name:"local" sep:","`
	ExtraRules    bool     `help:"enable gofumpt extra rules" name:"extra"`
	Fast          bool     `help:"skip type loading (faster, less accurate imports)" name:"fast"`
	Version       kong.VersionFlag `help:"print version and exit" name:"version" vars:"version=${version}"`

	Paths []string `arg:"" optional:"" help:"Paths to format (directories or files)."`
}

func main() {
	cli := &CLI{}
	kong.Parse(cli,
		kong.Name("gomop"),
		kong.Description("A unified Go formatter combining gofumpt, golines, and goimports."),
		kong.UsageOnError(),
		kong.Vars{"version": version},
	)

	if len(cli.Paths) == 0 {
		// Read from stdin
		src, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading stdin: %v\n", err)
			os.Exit(1)
		}
		result, err := formatSource(cli, "<stdin>", src)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(string(result))
		return
	}

	var expandedPaths []string
	for _, p := range cli.Paths {
		if strings.ContainsAny(p, "*?[]{}") {
			matches, err := doublestar.FilepathGlob(p)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error matching pattern %s: %v\n", p, err)
				continue
			}
			expandedPaths = append(expandedPaths, matches...)
		} else {
			expandedPaths = append(expandedPaths, p)
		}
	}

	exitCode := 0
	numWorkers := runtime.NumCPU()
	pathsChan := make(chan string, numWorkers*2)
	errChan := make(chan error, numWorkers*2)
	var wg sync.WaitGroup

	// Start workers
	for range numWorkers {
		wg.Go(func() {
			for p := range pathsChan {
				if err := processPath(cli, p); err != nil {
					errChan <- fmt.Errorf("error processing %s: %v", p, err)
				}
			}
		})
	}

	// Feed paths
	go func() {
		for _, path := range expandedPaths {
			pathsChan <- path
		}
		close(pathsChan)
	}()

	// Error collector
	go func() {
		for err := range errChan {
			fmt.Fprintln(os.Stderr, err)
			exitCode = 1
		}
	}()

	wg.Wait()
	close(errChan)
	os.Exit(exitCode)
}

func formatSource(cli *CLI, filename string, src []byte) ([]byte, error) {
	modPath := cli.ModulePath
	if modPath == "" && filename != "<stdin>" {
		modPath = stdlib.FindModulePath(filename)
	}

	opts := format.Options{
		LineLength:    cli.LineLength,
		TabWidth:      cli.TabWidth,
		GoVersion:     cli.GoVersion,
		ModulePath:    modPath,
		LocalPrefixes: cli.LocalPrefixes,
		ExtraRules:    cli.ExtraRules,
		SkipTypeInfo:  cli.Fast,
	}

	f := format.New(opts)
	return f.Format(filename, src)
}

func processPath(cli *CLI, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if info.IsDir() {
		return processDir(cli, path)
	}
	return processFile(cli, path, info.Mode())
}

func processDir(cli *CLI, dir string) error {
	// Load ignore patterns from .gomopignore
	ignore := newIgnoreMatcher(dir)

	return godirwalk.Walk(dir, &godirwalk.Options{
		Callback: func(path string, de *godirwalk.Dirent) error {
			if de.IsDir() {
				if de.Name() == "vendor" || de.Name() == "testdata" || strings.HasPrefix(de.Name(), ".") {
					return filepath.SkipDir
				}
				// Check if directory matches ignore pattern
				if ignore.match(path) {
					return filepath.SkipDir
				}
				return nil
			}

			if !strings.HasSuffix(path, ".go") {
				return nil
			}

			// Check if file matches ignore pattern
			if ignore.match(path) {
				return nil
			}

			// godirwalk provides the mode type, but for a full FileMode
			// we still need a Stat if we want to preserve permissions (-w)
			// But for just reading, we don't need it.
			// However, since processFile needs mode for WriteFile, we keep it
			// but we could optimize by only statting if cli.Write is true.
			var mode os.FileMode = 0644
			if cli.Write {
				info, err := os.Stat(path)
				if err != nil {
					return err
				}
				mode = info.Mode()
			}

			return processFile(cli, path, mode)
		},
		Unsorted: true,
	})
}

func processFile(cli *CLI, path string, mode os.FileMode) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// Skip generated files
	if isGenerated(src) {
		return nil
	}

	result, err := formatSource(cli, path, src)
	if err != nil {
		return err
	}

	if bytes.Equal(src, result) {
		return nil
	}

	switch {
	case cli.List:
		fmt.Println(path)
	case cli.Write:
		return os.WriteFile(path, result, mode)
	case cli.Diff:
		diffText, err := diff(src, result, path)
		if err != nil {
			return err
		}
		if len(diffText) > 0 {
			fmt.Print(string(diffText))
		}
	default:
		fmt.Print(string(result))
	}

	return nil
}

// generatedRx matches Go's standard generated file markers.
// See https://go.dev/s/generatedcode
var generatedRx = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)

// isGenerated reports whether src contains a Go generated file marker.
func isGenerated(src []byte) bool {
	// Only check the first 1KB to avoid scanning huge files
	limit := len(src)
	if limit > 1024 {
		limit = 1024
	}
	return generatedRx.Match(src[:limit])
}
