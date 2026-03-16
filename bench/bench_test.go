// Package bench provides comparative benchmarks for gomop vs goimports+golines+gofumpt.
//
// Run with:
//
//	go test -bench=. -benchmem ./bench/
//	go test -bench=. -benchmem -benchtime=10s ./bench/
package bench

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nijaru/gomop/internal/format"
)

// testFiles returns paths to benchmark input files, skipping if not found.
func testFiles(b *testing.B) map[string][]byte {
	b.Helper()
	files := map[string][]byte{}

	// Small: comprehensive test fixture (~40 lines)
	if src, err := os.ReadFile(filepath.Join("..", "internal", "format", "testdata", "comprehensive.input")); err == nil {
		files["small"] = src
	}

	// Medium: format.go itself (~1450 lines)
	if src, err := os.ReadFile(filepath.Join("..", "internal", "format", "format.go")); err == nil {
		files["medium"] = src
	}

	// Large: dedicated benchmark file (~500+ lines)
	if src, err := os.ReadFile(filepath.Join("..", "internal", "format", "testdata", "large.input")); err == nil {
		files["large"] = src
	}

	// XL: stdlib manifest (~18000 lines)
	if src, err := os.ReadFile(filepath.Join("..", "internal", "stdlib", "manifest.go")); err == nil {
		files["xl"] = src
	}

	return files
}

// =============================================================================
// gomop (internal API - no subprocess overhead)
// =============================================================================

func BenchmarkGomop(b *testing.B) {
	files := testFiles(b)
	opts := format.DefaultOptions()

	for name, src := range files {
		b.Run(name, func(b *testing.B) {
			f := format.New(opts)
			b.SetBytes(int64(len(src)))
			b.ResetTimer()

			for b.Loop() {
				_, err := f.Format("bench.go", src)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// =============================================================================
// Pipeline: goimports -> golines -> gofumpt (subprocess)
// =============================================================================

// checkTool verifies a CLI tool exists in PATH, skips benchmark if missing.
func checkTool(b *testing.B, name string) string {
	b.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		b.Skipf("%s not found in PATH", name)
	}
	return path
}

func BenchmarkGoimports(b *testing.B) {
	files := testFiles(b)
	checkTool(b, "goimports")

	for name, src := range files {
		b.Run(name, func(b *testing.B) {
			tmpFile := writeTemp(b, src)
			defer os.Remove(tmpFile)

			b.SetBytes(int64(len(src)))
			b.ResetTimer()

			for b.Loop() {
				cmd := exec.Command("goimports", tmpFile)
				var out bytes.Buffer
				cmd.Stdout = &out
				if err := cmd.Run(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkGolines(b *testing.B) {
	files := testFiles(b)
	checkTool(b, "golines")

	for name, src := range files {
		b.Run(name, func(b *testing.B) {
			tmpFile := writeTemp(b, src)
			defer os.Remove(tmpFile)

			b.SetBytes(int64(len(src)))
			b.ResetTimer()

			for b.Loop() {
				cmd := exec.Command("golines", "-m", "100", tmpFile)
				var out bytes.Buffer
				cmd.Stdout = &out
				if err := cmd.Run(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkGofumpt(b *testing.B) {
	files := testFiles(b)
	checkTool(b, "gofumpt")

	for name, src := range files {
		b.Run(name, func(b *testing.B) {
			tmpFile := writeTemp(b, src)
			defer os.Remove(tmpFile)

			b.SetBytes(int64(len(src)))
			b.ResetTimer()

			for b.Loop() {
				cmd := exec.Command("gofumpt", tmpFile)
				var out bytes.Buffer
				cmd.Stdout = &out
				if err := cmd.Run(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkPipeline runs the full pipeline: goimports | golines | gofumpt
func BenchmarkPipeline(b *testing.B) {
	files := testFiles(b)
	checkTool(b, "goimports")
	checkTool(b, "golines")
	checkTool(b, "gofumpt")

	for name, src := range files {
		b.Run(name, func(b *testing.B) {
			tmpFile := writeTemp(b, src)
			defer os.Remove(tmpFile)

			b.SetBytes(int64(len(src)))
			b.ResetTimer()

			for b.Loop() {
				// goimports
				var out1 bytes.Buffer
				cmd1 := exec.Command("goimports", tmpFile)
				cmd1.Stdout = &out1
				if err := cmd1.Run(); err != nil {
					b.Fatal("goimports:", err)
				}

				// golines (reads stdin with - flag)
				var out2 bytes.Buffer
				cmd2 := exec.Command("golines", "-m", "100")
				cmd2.Stdin = &out1
				cmd2.Stdout = &out2
				if err := cmd2.Run(); err != nil {
					b.Fatal("golines:", err)
				}

				// gofumpt (reads stdin with - flag)
				var out3 bytes.Buffer
				cmd3 := exec.Command("gofumpt")
				cmd3.Stdin = &out2
				cmd3.Stdout = &out3
				if err := cmd3.Run(); err != nil {
					b.Fatal("gofumpt:", err)
				}
			}
		})
	}
}

// =============================================================================
// gomop CLI (subprocess - fair comparison against pipeline)
// =============================================================================

func BenchmarkGomopCLI(b *testing.B) {
	files := testFiles(b)

	// Build gomop binary
	binPath := filepath.Join(b.TempDir(), "gomop")
	build := exec.Command("go", "build", "-o", binPath, "./cmd/gomop")
	build.Dir = filepath.Join("..")
	if out, err := build.CombinedOutput(); err != nil {
		b.Fatalf("failed to build gomop: %v\n%s", err, out)
	}

	for name, src := range files {
		b.Run(name, func(b *testing.B) {
			tmpFile := writeTemp(b, src)
			defer os.Remove(tmpFile)

			b.SetBytes(int64(len(src)))
			b.ResetTimer()

			for b.Loop() {
				cmd := exec.Command(binPath, tmpFile)
				var out bytes.Buffer
				cmd.Stdout = &out
				if err := cmd.Run(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// =============================================================================
// Helpers
// =============================================================================

func writeTemp(b *testing.B, src []byte) string {
	b.Helper()
	f, err := os.CreateTemp("", "gomop-bench-*.go")
	if err != nil {
		b.Fatal(err)
	}
	if _, err := f.Write(src); err != nil {
		f.Close()
		os.Remove(f.Name())
		b.Fatal(err)
	}
	f.Close()
	return f.Name()
}
