package goproc

import (
	"bytes"
	"debug/elf"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Go forks with CLONE_VFORK: the parent thread stays blocked until the child's
// execve has read the binary, and a GC stop-the-world in that window stalls
// every goroutine. Read what execve reads (the binary and its interpreters)
// before Start, so a lazily loaded binary only delays its own process.
func preloadExecutable(cmd *exec.Cmd) {
	path := cmd.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(cmd.Dir, path)
	}
	for depth := 0; path != "" && depth < 4; depth++ {
		path = interpreterOf(path)
	}
}

// interpreterOf returns the program a script's #! line or an ELF binary's
// PT_INTERP names, or "".
func interpreterOf(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	head := make([]byte, 256)
	n, _ := io.ReadFull(f, head)
	if line, ok := bytes.CutPrefix(head[:n], []byte("#!")); ok {
		line, _, _ = bytes.Cut(line, []byte("\n"))
		if fields := bytes.Fields(line); len(fields) > 0 {
			return string(fields[0])
		}
		return ""
	}

	bin, err := elf.NewFile(f)
	if err != nil {
		return ""
	}
	for _, p := range bin.Progs {
		if p.Type == elf.PT_INTERP {
			name, _ := io.ReadAll(p.Open())
			return string(bytes.TrimRight(name, "\x00"))
		}
	}
	return ""
}
