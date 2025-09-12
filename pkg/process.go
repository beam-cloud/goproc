package goproc

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type Process struct {
	ctx       context.Context
	pid       int
	exitCode  int
	cmd       *exec.Cmd
	stdoutBuf *SafeBuffer
	stderrBuf *SafeBuffer
	mu        sync.Mutex
}

func NewProcess(ctx context.Context) (*Process, error) {
	return &Process{ctx: ctx, pid: -1, exitCode: -1, mu: sync.Mutex{}}, nil
}

func (p *Process) Exec(args []string, cwd string, env []string, wait bool) (int, error) {
	cmd := exec.CommandContext(context.Background(), args[0], args[1:]...)
	cmd.Dir = cwd
	cmd.Env = env

	p.cmd = cmd
	p.stdoutBuf = &SafeBuffer{}
	p.stderrBuf = &SafeBuffer{}
	p.cmd.Stdout = p.stdoutBuf
	p.cmd.Stderr = p.stderrBuf

	err := p.cmd.Start()
	if err != nil {
		return -1, err
	}

	p.pid = p.cmd.Process.Pid

	if wait {
		// Wait synchronously
		err = p.cmd.Wait()
		if err != nil {
			return p.pid, err
		}
		p.exitCode = p.cmd.ProcessState.ExitCode()
	} else {
		// Monitor the process in background
		go func() {
			err := p.cmd.Wait()
			if err != nil {
				p.exitCode = 1
				return
			}

			if p.cmd.ProcessState != nil {
				p.exitCode = p.cmd.ProcessState.ExitCode()
			}
		}()
	}

	return p.pid, nil
}

func (p *Process) Wait() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd == nil {
		return -1, ErrProcessNotFound
	}

	// If we already have an exit code, the process was already waited on
	if p.exitCode != -1 {
		return p.exitCode, nil
	}

	err := p.cmd.Wait()

	// Set exit code if wait succeeded
	if err == nil && p.cmd.ProcessState != nil {
		p.exitCode = p.cmd.ProcessState.ExitCode()
	}

	// If error is "already waited", that's ok - get exit code from ProcessState
	if err != nil && strings.Contains(err.Error(), "wait") && p.cmd.ProcessState != nil {
		p.exitCode = p.cmd.ProcessState.ExitCode()
		return p.exitCode, nil
	}

	if err != nil {
		return p.exitCode, err
	}

	return p.exitCode, nil
}

func (p *Process) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd == nil {
		return ErrProcessNotFound
	}

	return p.cmd.Process.Kill()
}

func (p *Process) Signal(sig os.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd == nil {
		return ErrProcessNotFound
	}

	return p.cmd.Process.Signal(sig)
}

func (p *Process) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd == nil {
		return false
	}

	if p.cmd.ProcessState == nil {
		return true
	}

	return !p.cmd.ProcessState.Exited()
}

func (p *Process) ExitCode() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd == nil {
		return -1
	}

	return p.exitCode
}

func (p *Process) Logs() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd == nil {
		return ""
	}

	return p.stdoutBuf.StringAndReset() + "\n" + p.stderrBuf.StringAndReset()
}

func (p *Process) Stdout() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd == nil {
		return ""
	}

	return p.stdoutBuf.StringAndReset()
}

func (p *Process) Stderr() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd == nil {
		return ""
	}

	return p.stderrBuf.StringAndReset()
}
