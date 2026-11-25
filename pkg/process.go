package goproc

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

const (
	ProcessStreamStdout = "stdout"
	ProcessStreamStderr = "stderr"
	// A fractional CPU may not schedule the output copier within one quota
	// period. Retain a bounded drain deadline without truncating normal output.
	processWaitDelay = 2 * time.Second
)

type ProcessLogSink interface {
	WriteProcessLog(stream string, data []byte) error
}

type Process struct {
	ctx       context.Context
	pid       int
	exitCode  int
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	stdoutBuf *SafeBuffer
	stderrBuf *SafeBuffer
	mu        sync.Mutex
	waitOnce  sync.Once
	waitDone  chan struct{}
	waitErr   error
	started   chan struct{}
	startOnce sync.Once
	drains    []*outputDrain
	drained   chan error
}

func NewProcess(ctx context.Context) (*Process, error) {
	return &Process{
		ctx:      ctx,
		pid:      -1,
		exitCode: -1,
		mu:       sync.Mutex{},
		waitDone: make(chan struct{}),
		started:  make(chan struct{}),
	}, nil
}

func (p *Process) Exec(args []string, cwd string, env []string, wait bool) (int, error) {
	return p.exec(args, cwd, env, wait, nil)
}

func (p *Process) ExecWithLogSink(args []string, cwd string, env []string, wait bool, sink ProcessLogSink) (int, error) {
	return p.exec(args, cwd, env, wait, sink)
}

func (p *Process) exec(args []string, cwd string, env []string, wait bool, sink ProcessLogSink) (int, error) {
	baseCtx := p.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	cmdCtx, cancel := context.WithCancel(baseCtx)

	cmd := exec.CommandContext(cmdCtx, args[0], args[1:]...)
	cmd.Dir = cwd
	cmd.Env = env
	configureCommandProcessGroup(cmd)
	cmd.Cancel = func() error {
		return killProcessTree(cmd.Process)
	}
	cmd.WaitDelay = processWaitDelay

	p.cmd = cmd
	p.cancel = cancel
	p.stdoutBuf = &SafeBuffer{}
	p.stderrBuf = &SafeBuffer{}
	var stdout, stderr io.Writer = p.stdoutBuf, p.stderrBuf
	if sink != nil {
		stdout = &processLogWriter{
			stream: ProcessStreamStdout,
			buffer: p.stdoutBuf,
			sink:   sink,
			kill:   p.killFromLogWriter,
		}
		stderr = &processLogWriter{
			stream: ProcessStreamStderr,
			buffer: p.stderrBuf,
			sink:   sink,
			kill:   p.killFromLogWriter,
		}
	}

	preloadExecutable(p.cmd)
	err := p.startWithOutput(stdout, stderr)
	if err != nil {
		return -1, err
	}

	p.pid = p.cmd.Process.Pid
	p.markStarted()

	if wait {
		p.waitForExit()
		if err := p.ctx.Err(); err != nil {
			return p.pid, err
		}
		if p.waitErr != nil {
			return p.pid, p.waitErr
		}
	} else {
		go p.waitForExit()
	}

	return p.pid, nil
}

// startWithOutput starts the process with pipes that outlive it, for background
// children that inherit them.
func (p *Process) startWithOutput(stdout, stderr io.Writer) error {
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer stdoutW.Close()
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		return err
	}
	defer stderrW.Close()

	p.cmd.Stdout, p.cmd.Stderr = stdoutW, stderrW
	if err := p.start(); err != nil {
		stdoutR.Close()
		stderrR.Close()
		return err
	}

	p.drains = []*outputDrain{{r: stdoutR, dst: stdout}, {r: stderrR, dst: stderr}}
	p.drained = make(chan error, len(p.drains))
	for _, d := range p.drains {
		go d.run(p.drained)
	}
	return nil
}

// waitForOutput waits for an exited process's pipes to close, for at most
// processWaitDelay: a background child can hold them for as long as it runs.
func (p *Process) waitForOutput() error {
	timer := time.NewTimer(processWaitDelay)
	defer timer.Stop()
	var firstErr error
	for range p.drains {
		select {
		case err := <-p.drained:
			if firstErr == nil {
				firstErr = err
			}
		case <-timer.C:
			stopDrains(p.drains)
			return exec.ErrWaitDelay
		}
	}
	return firstErr
}

func (p *Process) markStarted() {
	p.startOnce.Do(func() {
		close(p.started)
	})
}

type processLogWriter struct {
	stream string
	buffer io.Writer
	sink   ProcessLogSink
	kill   func()
}

func (w *processLogWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	data := append([]byte(nil), p...)
	if err := w.sink.WriteProcessLog(w.stream, data); err != nil {
		if w.kill != nil {
			w.kill()
		}
		return 0, err
	}

	return w.buffer.Write(p)
}

func (p *Process) killFromLogWriter() {
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	p.waitStarted()

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	// Wait also drains inherited pipes. A reaped child may still have live
	// descendants holding those pipes, so stop the group until drain ends.
	// ProcessState is written by Cmd.Wait without holding p.mu.
	select {
	case <-p.waitDone:
		return
	default:
	}

	_ = killProcessTree(p.cmd.Process)
}

func (p *Process) waitStarted() {
	if p.started == nil {
		return
	}

	select {
	case <-p.started:
	case <-time.After(time.Second):
	}
}

func (p *Process) Wait() (int, error) {
	p.mu.Lock()
	if p.cmd == nil {
		p.mu.Unlock()
		return -1, ErrProcessNotFound
	}
	p.mu.Unlock()

	p.waitForExit()

	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitCode, p.waitErr
}

func (p *Process) waitForExit() {
	p.waitOnce.Do(func() {
		err := p.cmd.Wait()
		if outputErr := p.waitForOutput(); err == nil {
			err = outputErr
		}

		p.mu.Lock()
		defer p.mu.Unlock()
		defer close(p.waitDone)

		p.waitErr = err
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			p.waitErr = nil
		}
		if p.cmd.ProcessState != nil {
			p.exitCode = processExitCode(p.cmd.ProcessState)
			// A successful child exit does not make failed output collection a
			// success. Status and Wait must agree when the drain deadline expires.
			if p.exitCode == 0 && p.waitErr != nil {
				p.exitCode = 1
			}
			return
		}

		if err != nil {
			p.exitCode = 1
		}
	})

	<-p.waitDone
}

func (p *Process) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd == nil {
		return ErrProcessNotFound
	}

	return killProcessTree(p.cmd.Process)
}

func (p *Process) Signal(sig os.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd == nil {
		return ErrProcessNotFound
	}

	return signalProcessTree(p.cmd.Process, sig)
}

func (p *Process) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd == nil {
		return false
	}

	select {
	case <-p.waitDone:
		return false
	default:
		return true
	}
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
