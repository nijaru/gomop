package format

import (
	"os"
	"testing"
)

func BenchmarkFormat(b *testing.B) {
	src, err := os.ReadFile("format.go")
	if err != nil {
		b.Fatal(err)
	}

	f := New(DefaultOptions())
	b.ResetTimer()

	for b.Loop() {
		_, err := f.Format("format.go", src)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFormatLarge(b *testing.B) {
	src, err := os.ReadFile("testdata/large.input")
	if err != nil {
		b.Fatal(err)
	}

	f := New(DefaultOptions())
	b.ResetTimer()

	for b.Loop() {
		_, err := f.Format("testdata/large.input", src)
		if err != nil {
			b.Fatal(err)
		}
	}
}
