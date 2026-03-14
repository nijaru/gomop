package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/karrick/godirwalk"
	"github.com/nijaru/goformat/internal/format"
)

var (
	// Version is set by goreleaser
	version = "dev"
)

type CLI struct {
	Write         bool     `help:"write result to (source) file instead of stdout" short:"w"`
	List          bool     `help:"list files whose formatting differs from goformat's" short:"l"`
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
		kong.Name("goformat"),
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
	for _, path := range expandedPaths {
		if err := processPath(cli, path); err != nil {
			fmt.Fprintf(os.Stderr, "error processing %s: %v\n", path, err)
			exitCode = 1
		}
	}
	os.Exit(exitCode)
}

func formatSource(cli *CLI, filename string, src []byte) ([]byte, error) {
	opts := format.Options{
		LineLength:    cli.LineLength,
		TabWidth:      cli.TabWidth,
		GoVersion:     cli.GoVersion,
		ModulePath:    cli.ModulePath,
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
	return godirwalk.Walk(dir, &godirwalk.Options{
		Callback: func(path string, de *godirwalk.Dirent) error {
			// Skip directories
			if de.IsDir() {
				// Skip vendor and testdata directories
				if de.Name() == "vendor" || de.Name() == "testdata" || strings.HasPrefix(de.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}

			// Only process Go files
			if !strings.HasSuffix(path, ".go") {
				return nil
			}

			// Get file mode
			info, err := os.Stat(path)
			if err != nil {
				return err
			}

			return processFile(cli, path, info.Mode())
		},
		Unsorted: true, // Faster
	})
}

func processFile(cli *CLI, path string, mode os.FileMode) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
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
