package goproc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInterpreterOf(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "script")
	if err := os.WriteFile(script, []byte("#!/bin/sh -eu\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := interpreterOf(script); got != "/bin/sh" {
		t.Fatalf("script interpreter = %q", got)
	}
	if got := interpreterOf(filepath.Join(dir, "missing")); got != "" {
		t.Fatalf("missing file interpreter = %q", got)
	}

	// A dynamically linked sh names its loader, which is static.
	if loader := interpreterOf("/bin/sh"); loader != "" {
		if _, err := os.Stat(loader); err != nil {
			t.Fatal(err)
		}
		if got := interpreterOf(loader); got != "" {
			t.Fatalf("loader interpreter = %q", got)
		}
	}
}
