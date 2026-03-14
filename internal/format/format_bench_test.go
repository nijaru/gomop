package format

import (
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkFormat measures formatting performance
func BenchmarkFormat(b *testing.B) { // Use the formatter's own source as benchmark input
	src, err := os.ReadFile("format.go")
	if err != nil {
		b.Fatal(err)
	}

	f := New(DefaultOptions())
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := f.Format("format.go", src)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFormatLarge measures formatting on larger files
func BenchmarkFormatLarge(b *testing.B) { // Find a large Go file in the project
	var largeFile string
	var maxSize int64

	filepath.Walk(
		"..",
		func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || filepath.Ext(path) != ".go" {
				return nil
			}
			if info.Size() > maxSize {
				maxSize = info.Size()
				largeFile = path
			}
			return nil
		},
	)

	if largeFile == "" {
		b.Skip("no Go files found")
	}

	src, err := os.ReadFile(largeFile)
	if err != nil {
		b.Fatal(err)
	}

	f := New(DefaultOptions())
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := f.Format(largeFile, src)
		if err != nil {
			b.Fatal(err)
		}
	}
}
