package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Every documented example must be a valid configuration.
func TestExamplesParse(t *testing.T) {
	files, err := filepath.Glob("../../examples/*/uki.yaml")
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, "../../uki.yaml", "../cli/templates/uki.yaml")
	if len(files) < 5 {
		t.Fatalf("expected example configs, found %v", files)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(b); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}
