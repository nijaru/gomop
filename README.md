# gomop

> ⚠️ **Early Development**: Expect bugs, missing features, and breaking changes. Not recommended for production use yet.

A high-performance, unified Go formatter that combines `gofumpt`, `golines`, and `goimports` into a single tool.

## Features

- **Import fixing** — Adds missing stdlib imports, removes unused imports
- **Import grouping** — Strict 3-block grouping: stdlib, third-party, local
- **Line shortening** — Splits long lines based on configurable width
  - Function calls and composite literals
  - String literals (word-aware splitting)
  - Method chains (dot-first style)
  - Boolean expression splitting
- **Gofumpt rules** — Stricter formatting for consistent style
  - Short case clauses collapsed to single line
  - No empty lines around function bodies
  - `interface{}` → `any` conversion
  - Struct tag alignment
  - Octal literal modernization (`0644` → `0o644`)
  - Comment whitespace normalization
  - Function parameter grouping
  - Single-statement function body collapsing
- **Fast by default** — No `packages.Load` overhead; O(1) stdlib import resolution
- **Fuzz-tested** — 1M+ fuzz iterations for correctness and idempotency

## Installation

```bash
go install github.com/nijaru/gomop/cmd/gomop@latest
```

## Usage

```bash
# Format files (prints to stdout)
gomop file.go

# Write changes in-place
gomop -w file.go

# List files that need formatting  
gomop -l ./...

# Show diffs
gomop -d file.go

# Use glob patterns
gomop '**/*.go'
```

## Options

| Flag | Default | Description |
|------|---------|-------------|
| `-w, --write` | false | Write result to source file |
| `-l, --list` | false | List files whose formatting differs |
| `-d, --diff` | false | Display diffs |
| `-m, --line-length` | 100 | Maximum line length |
| `-t, --tab-width` | 4 | Tab width |
| `--modpath` | | Module path for import grouping |
| `--local` | | Comma-separated local import prefixes |
| `--fast` | false | Skip sibling file scan for import resolution |
| `--resolve` | false | Load full type info for third-party import resolution |
| `--version` | | Print version and exit |

## Performance

gomop uses tiered import resolution. By default, it never calls `packages.Load`:

1. **AST name matching** — Match `pkg.Symbol` to existing imports
2. **Stdlib lookup** — O(1) in-memory reverse index
3. **Sibling files** — Parse other files in directory (skip with `--fast`)
4. **Full type info** — Only with `--resolve` flag (opt-in)

This avoids the ~40ms `packages.Load` penalty that `goimports` pays on every file.

## License

MIT
