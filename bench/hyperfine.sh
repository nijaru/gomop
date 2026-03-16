#!/usr/bin/env bash
#
# gomop comparative benchmarks using hyperfine.
#
# Compares gomop (single pass) vs goimports + golines + gofumpt (pipeline)
# on multiple file sizes.
#
# Usage:
#   ./bench/hyperfine.sh              # Run all benchmarks
#   ./bench/hyperfine.sh --warmup 5   # Custom warmup count
#   ./bench/hyperfine.sh --export json results.json
#
# Prerequisites:
#   brew install hyperfine
#   go install golang.org/x/tools/cmd/goimports@latest
#   go install mvdan.cc/gofumpt@latest
#   go install github.com/segmentio/golines@latest

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
TESTDATA_DIR="$PROJECT_DIR/internal/format/testdata"
BUILD_DIR=$(mktemp -d)
trap "rm -rf $BUILD_DIR" EXIT

echo "Building gomop..."
go build -o "$BUILD_DIR/gomop" "$PROJECT_DIR/cmd/gomop"

for tool in goimports golines gofumpt hyperfine; do
    if ! command -v "$tool" &>/dev/null; then
        echo "Error: $tool not found in PATH"
        exit 1
    fi
done

SMALL="$TESTDATA_DIR/comprehensive.input"
MEDIUM="$PROJECT_DIR/internal/format/format.go"
LARGE="$TESTDATA_DIR/large.input"
XL="$PROJECT_DIR/internal/stdlib/manifest.go"

# Extra args passed through
EXTRA_ARGS="${*:-}"

run_bench() {
    local label="$1"
    local file="$2"

    echo ""
    echo "━━━ $label ($(wc -l < "$file" | tr -d ' ') lines, $(wc -c < "$file" | tr -d ' ') bytes) ━━━"
    echo ""

    hyperfine \
        --warmup 3 \
        --min-runs 10 \
        --export-markdown "/dev/stdout" \
        \
        --command-name "gomop" \
        "cp '$file' '$BUILD_DIR/t.go' && '$BUILD_DIR/gomop' -w '$BUILD_DIR/t.go'" \
        \
        --command-name "pipeline (goimports|golines|gofumpt)" \
        "goimports '$file' | golines -m 100 | gofumpt > '$BUILD_DIR/t.go'" \
        \
        --command-name "goimports+gofumpt (no line shortening)" \
        "goimports '$file' | gofumpt > '$BUILD_DIR/t.go'" \
        \
        --command-name "goimports only" \
        "goimports '$file' > '$BUILD_DIR/t.go'" \
        \
        --command-name "gofumpt only" \
        "gofumpt '$file' > '$BUILD_DIR/t.go'" \
        \
        --command-name "golines only" \
        "golines -m 100 '$file' > '$BUILD_DIR/t.go'" \
        \
        $EXTRA_ARGS
}

echo "╔══════════════════════════════════════════════════════════════╗"
echo "║         gomop vs goimports + golines + gofumpt              ║"
echo "╠══════════════════════════════════════════════════════════════╣"
echo "║  Small:   ~40 lines   (comprehensive test fixture)          ║"
echo "║  Medium:  ~1450 lines (format.go)                           ║"
echo "║  Large:   ~500 lines  (large test fixture)                  ║"
echo "║  XL:      ~18000 lines (stdlib manifest)                    ║"
echo "╚══════════════════════════════════════════════════════════════╝"

run_bench "Small (~40 lines)" "$SMALL"
run_bench "Medium (~1450 lines)" "$MEDIUM"
run_bench "Large (~500 lines)" "$LARGE"
run_bench "XL (~18000 lines)" "$XL"

echo ""
echo "Done. For JSON export, add: --export json results.json"
