package format

import (
	"testing"
)

func TestAddMissingStdlib(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "add_fmt",
			input: `package test

func main() {
	fmt.Println("hi")
	_ = 0
}
`,
			want: `package test

import "fmt"

func main() {
	fmt.Println("hi")
	_ = 0
}
`,
		},
		{
			name: "add_multiple_stdlib",
			input: `package test

func main() {
	fmt.Println("hi")
	_ = os.Args
	_ = json.Marshal(nil)
}
`,
			want: `package test

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	fmt.Println("hi")
	_ = os.Args
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
				got, err := f.Format("test.go", []byte(tt.input))
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
