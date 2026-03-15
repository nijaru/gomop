package main

import (
	"os"
	"strings"

	"github.com/aymanbagabas/go-udiff"
	"github.com/mattn/go-isatty"
)

var (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorCyan   = "\033[36m"
	colorYellow = "\033[33m"
	colorBold   = "\033[1m"
)

func diff(a, b []byte, path string) ([]byte, error) {
	d := udiff.Unified(path, path, string(a), string(b))

	// Check if we should colorize (stdout is a terminal)
	if isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd()) {
		return []byte(colorizeDiff(d)), nil
	}

	return []byte(d), nil
}

// colorizeDiff applies ANSI colors to a unified diff.
func colorizeDiff(diff string) string {
	var result strings.Builder
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			result.WriteString(colorBold)
			result.WriteString(line)
			result.WriteString(colorReset)
		case strings.HasPrefix(line, "+"):
			result.WriteString(colorGreen)
			result.WriteString(line)
			result.WriteString(colorReset)
		case strings.HasPrefix(line, "-"):
			result.WriteString(colorRed)
			result.WriteString(line)
			result.WriteString(colorReset)
		case strings.HasPrefix(line, "@@"):
			result.WriteString(colorCyan)
			result.WriteString(line)
			result.WriteString(colorReset)
		case strings.HasPrefix(line, "diff --git") || strings.HasPrefix(line, "index "):
			result.WriteString(colorYellow)
			result.WriteString(line)
			result.WriteString(colorReset)
		default:
			result.WriteString(line)
		}
		result.WriteString("\n")
	}

	return strings.TrimSuffix(result.String(), "\n")
}
