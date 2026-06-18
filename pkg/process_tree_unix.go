//go:build !windows

package goproc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureCommandProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessTree(process *os.Process) error {
	if process == nil {
		return ErrProcessNotFound
	}
	if err := syscall.Kill(-process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func signalProcessTree(process *os.Process, sig os.Signal) error {
	if process == nil {
		return ErrProcessNotFound
	}
	sysSig, ok := sig.(syscall.Signal)
	if !ok {
		return process.Signal(sig)
	}
	if err := syscall.Kill(-process.Pid, sysSig); err != syscall.ESRCH {
		return err
	}
	return process.Signal(sig)
}
