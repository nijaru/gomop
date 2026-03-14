# Project: gomop

## Overview
`gomop` is a high-performance, unified, single-pass Go formatter. It combines the functionality of `gofumpt` (strict formatting), `golines` (line shortening), and `goimports` (import fixing) into a single tool.

## Key Architecture & Concepts
- **AST Manipulation:** Uses `github.com/dave/dst` (Decorated Syntax Tree) instead of standard `go/ast`. This is critical to preserve comments, newlines, and other decorations during transformation.
- **Single Pass:** The tool performs a single walk over the `dst` tree to apply all transformations simultaneously for maximum performance.
- **Tiered Import Resolution:** To maintain sub-10ms performance, import resolution avoids `packages.Load` (which adds ~40ms overhead) unless absolutely necessary. It cascades through:
  1. AST-based detection (fastest).
  2. Sibling file pass (parses other files in the directory to find local globals).
  3. Pre-calculated stdlib reverse index (`internal/stdlib/manifest.go`, O(1) lookup).
  4. Full type info via `packages.Load` (last resort only).
- **CLI Infrastructure:** Uses `alecthomas/kong` for declarative CLI definition instead of standard `flag`.
- **File Processing:** Uses `karrick/godirwalk` for high-speed directory traversal and `bmatcuk/doublestar/v4` for glob matching (e.g., `**/*.go`).
- **Diffing:** Uses `aymanbagabas/go-udiff` for fast, color-ready unified diffs.

## Essential Commands
- **Test:** `go test ./...`
- **Fast Test:** `go test ./... -short`
- **Run:** `go run ./cmd/gomop <paths>`

## Code Organization
- `cmd/gomop/`: The CLI entry point. Defines flags via `kong` and handles parallel processing, file traversal, and output.
- `internal/format/`: The core formatter logic. `format.go` parses source into `dst`, applies the rules, and prints the result.
- `internal/stdlib/`: Contains the pre-calculated stdlib manifest for fast package lookups.
- `ai/`: Contains design docs (`DESIGN.md`), status tracking (`STATUS.md`), and architectural decision records (`DECISIONS.md`). Always consult these before making architectural changes.

## Conventions and Gotchas
- **Performance is Critical:** Any changes to the core formatting loop or file scanning must be highly optimized. The target is < 5ms for a single file and < 10ms for scanning 1000 files.
- **Do not use `go/ast` for modifications:** Always use `github.com/dave/dst` for AST manipulation to prevent losing comments.
- **Import Resolution:** Avoid unconditional `packages.Load` calls. Respect the tiered import resolution strategy to maintain performance.
- **Concurrency:** File processing in `cmd/gomop` is highly concurrent. Ensure any package-level state or shared resources introduced are thread-safe.
