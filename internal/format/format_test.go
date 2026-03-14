package format

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormat(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "long_line_composite",
			input: `package test

func main() {
	m := map[string]string{"first_key": "first_value", "second_key": "second_value", "third_key": "third_value"}
	_ = m
}
`,
			want: `package test

func main() {
	m := map[string]string{
		"first_key":  "first_value",
		"second_key": "second_value",
		"third_key":  "third_value",
	}
	_ = m
}
`,
		},
		{
			name: "long_function_call",
			input: `package test

func main() {
	println("very long string that exceeds the maximum line length limit for this formatter", "another long string argument")
}
`,
			want: `package test

func main() {
	println(
		"very long string that exceeds the maximum line length limit for this formatter",
		"another long string argument",
	)
}
`,
		},
		{
			name: "short_lines_unchanged",
			input: `package test

func add(a, b int) int {
	return a + b
}
`,
			want: `package test

func add(a, b int) int { return a + b }
`,
		},
		{
			name: "imports_grouped",
			input: `package test

import (
	"fmt"
	"bytes"
	"encoding/json"
)

func main() {
	fmt.Println("hello")
	_ = bytes.Buffer{}
	_ = json.Marshal(nil)
}
`,
			want: `package test

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func main() {
	fmt.Println("hello")
	_ = bytes.Buffer{}
	_ = json.Marshal(nil)
}
`,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				f := New(DefaultOptions())
				got, err := f.Format(tt.name+".go", []byte(tt.input))
				if err != nil {
					t.Fatalf("Format() error = %v", err)
				}

				if string(got) != tt.want {
					t.Errorf(
						"Format() mismatch:\n--- want\n+++ got\n%s",
						diff(tt.want, string(got)),
					)
				}
			},
		)
	}
}

func TestIdempotency(t *testing.T) {
	tests := []string{
		`package test

func main() {
	m := map[string]string{
		"first_key":  "first_value",
		"second_key": "second_value",
	}
	_ = m
}
`,
		`package test

import "fmt"

func main() {
	fmt.Println(
		"hello",
		"world",
	)
}
`,
	}

	for i, input := range tests {
		t.Run(
			"case_"+string(rune('0'+i)),
			func(t *testing.T) {
				f := New(DefaultOptions())

				first, err := f.Format("test.go", []byte(input))
				if err != nil {
					t.Fatalf("first Format() error = %v", err)
				}

				second, err := f.Format("test.go", first)
				if err != nil {
					t.Fatalf("second Format() error = %v", err)
				}

				if !bytes.Equal(first, second) {
					t.Errorf(
						"Format not idempotent:\n--- first\n+++ second\n%s",
						diff(string(first), string(second)),
					)
				}
			},
		)
	}
}

func TestGofmtCompatibility(t *testing.T) { // Output should be valid input to gofmt (no changes when run through gofmt)
	inputs := []string{
		`package test

func main() {
	m := map[string]string{
		"key": "value",
	}
	_ = m
}
`,
	}

	for i, input := range inputs {
		t.Run(
			"case_"+string(rune('0'+i)),
			func(t *testing.T) {
				f := New(DefaultOptions())

				formatted, err := f.Format("test.go", []byte(input))
				if err != nil {
					t.Fatalf("Format() error = %v", err)
				}

				// Run through gofmt
				gofmted, err := format.Source(formatted)
				if err != nil {
					t.Fatalf("gofmt error = %v", err)
				}

				if !bytes.Equal(formatted, gofmted) {
					t.Errorf(
						"Output not gofmt compatible:\n--- gomop\n+++ gofmt\n%s",
						diff(string(formatted), string(gofmted)),
					)
				}
			},
		)
	}
}

func TestGoldenFiles(t *testing.T) {
	testdata := filepath.Join("testdata")
	entries, err := os.ReadDir(testdata)
	if err != nil {
		t.Skip("no testdata directory")
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".input") {
			continue
		}

		name := strings.TrimSuffix(entry.Name(), ".input")
		t.Run(
			name,
			func(t *testing.T) {
				inputPath := filepath.Join(testdata, entry.Name())
				goldenPath := filepath.Join(testdata, name+".golden")

				input, err := os.ReadFile(inputPath)
				if err != nil {
					t.Fatalf("reading input: %v", err)
				}

				golden, err := os.ReadFile(goldenPath)
				if err != nil {
					t.Fatalf("reading golden: %v", err)
				}

				f := New(DefaultOptions())
				got, err := f.Format(name+".go", input)
				if err != nil {
					t.Fatalf("Format() error = %v", err)
				}

				if string(got) != string(golden) {
					t.Errorf(
						"Format() mismatch:\n--- golden\n+++ got\n%s",
						diff(string(golden), string(got)),
					)
				}
			},
		)
	}
}

func diff(a, b string) string {
	aLines := strings.Split(a, "\n")
	bLines := strings.Split(b, "\n")

	var buf strings.Builder
	for i := 0; i < len(aLines) || i < len(bLines); i++ {
		aLine := ""
		bLine := ""
		if i < len(aLines) {
			aLine = aLines[i]
		}
		if i < len(bLines) {
			bLine = bLines[i]
		}

		if aLine != bLine {
			if aLine != "" {
				buf.WriteString(fmt.Sprintf("-%d: %s\n", i+1, aLine))
			}
			if bLine != "" {
				buf.WriteString(fmt.Sprintf("+%d: %s\n", i+1, bLine))
			}
		}
	}
	return buf.String()
}
