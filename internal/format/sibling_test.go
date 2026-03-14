package format

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSiblingCheck(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "gomop-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a sibling file with a global variable that looks like a package
	siblingContent := `package pkg
var SiblingPkg = struct{ Func func() }{nil}
`
	err = os.WriteFile(filepath.Join(tmpDir, "sibling.go"), []byte(siblingContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create the main file that uses the sibling variable with selector
	mainContent := `package pkg

func main() {
	SiblingPkg.Func()
}
`
	// If the sibling check works, "SiblingPkg" should NOT be treated as a package call.

	f := New(DefaultOptions())
	got, err := f.Format(filepath.Join(tmpDir, "main.go"), []byte(mainContent))
	if err != nil {
		t.Fatalf("Format() error = %v", err)
	}

	// It might still format the function body to one line if it's short
	want := `package pkg

func main() { SiblingPkg.Func() }
`
	if string(got) != want {
		t.Errorf("Format() mismatch:\n--- want\n%s\n--- got\n%s", want, string(got))
	}
}
