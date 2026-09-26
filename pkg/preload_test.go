package goproc

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReadExecutableHeadReturnsScriptInterpreter(t *testing.T) {
	script := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(script, []byte("#!/bin/sh -eu\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := readExecutableHead(script); got != "/bin/sh" {
		t.Fatalf("interpreter = %q, want /bin/sh", got)
	}
}

func TestReadExecutableHeadReturnsELFInterpreter(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	binary, err := elf.Open(sh)
	if err != nil {
		t.Skipf("%s is not an ELF binary", sh)
	}
	defer binary.Close()
	dynamic := false
	for _, prog := range binary.Progs {
		dynamic = dynamic || prog.Type == elf.PT_INTERP
	}
	if !dynamic {
		t.Skipf("%s is statically linked", sh)
	}

	interpreter := readExecutableHead(sh)
	if interpreter == "" {
		t.Fatalf("no interpreter read from %s", sh)
	}
	if _, err := os.Stat(interpreter); err != nil {
		t.Fatalf("interpreter %q: %v", interpreter, err)
	}
	if next := readExecutableHead(interpreter); next != "" {
		t.Fatalf("loader %q names interpreter %q", interpreter, next)
	}
}

func TestReadExecutableHeadIgnoresOtherFiles(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(data, []byte("not an executable"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := readExecutableHead(data); got != "" {
		t.Fatalf("interpreter = %q, want none", got)
	}
	if got := readExecutableHead(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Fatalf("interpreter = %q, want none", got)
	}
}
