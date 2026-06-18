//go:build windows

package goproc

import (
	"os"
	"os/exec"
)

func configureCommandProcessGroup(cmd *exec.Cmd) {}

func killProcessTree(process *os.Process) error {
	if process == nil {
		return ErrProcessNotFound
	}
	return process.Kill()
}

func signalProcessTree(process *os.Process, sig os.Signal) error {
	if process == nil {
		return ErrProcessNotFound
	}
	return process.Signal(sig)
}
