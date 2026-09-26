package goproc

import (
	"bytes"
	"debug/elf"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

const maxInterpreterDepth = 4

// preloadExecutable reads what execve reads before it releases the parent:
// the head of the binary and of each interpreter it names. Go starts
// processes with CLONE_VFORK and the suspended thread keeps its P, so a
// stop-the-world that begins while a lazily loaded binary is still being
// read stalls every goroutine until that read completes.
func preloadExecutable(cmd *exec.Cmd) {
	if cmd.Err != nil {
		return
	}
	path := cmd.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(cmd.Dir, path)
	}
	for i := 0; i < maxInterpreterDepth && path != ""; i++ {
		path = readExecutableHead(path)
	}
}

// readExecutableHead reads the start of path and returns the interpreter it
// names: a script's #! program or an ELF binary's PT_INTERP loader.
func readExecutableHead(path string) string {
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

	binary, err := elf.NewFile(f)
	if err != nil {
		return ""
	}
	for _, prog := range binary.Progs {
		if prog.Type != elf.PT_INTERP {
			continue
		}
		name, err := io.ReadAll(prog.Open())
		if err != nil {
			return ""
		}
		return string(bytes.TrimRight(name, "\x00"))
	}
	return ""
}
