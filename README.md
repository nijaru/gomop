# gomop

A high-performance, unified Go formatter that combines `gofumpt`, `golines`, and `goimports` into a single tool.

## Features

- **Import fixing** - Adds missing imports and removes unused ones
- **Line shortening** - Splits long lines based on configurable width
- **Strict formatting** - Applies `gofumpt` rules for consistent style
- **Single pass** - All transformations in one AST walk for speed
- **Fast** - Sub-10ms formatting for typical files

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
| `--go` | go1.24 | Go version for formatting |
| `--modpath` | | Module path for import grouping |
| `--local` | | Comma-separated local import prefixes |
| `--extra` | false | Enable gofumpt extra rules |
| `--fast` | false | Skip type loading (faster, less accurate) |
| `--version` | | Print version and exit |

## Performance

gomop uses a tiered import resolution strategy to avoid the ~40ms overhead of `packages.Load`:

1. **AST-only pass** - Fast, always runs
2. **Sibling files** - Parse other files in directory
3. **Stdlib lookup** - O(1) in-memory index
4. **Full type info** - Only as last resort

Result: ~6.6ms for a 30K file (8.3x faster than baseline).

## License

MIT