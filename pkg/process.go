package goproc

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	ProcessStreamStdout = "stdout"
	ProcessStreamStderr = "stderr"
	processWaitDelay    = 100 * time.Millisecond
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
	p.cmd.Stdout = p.stdoutBuf
	p.cmd.Stderr = p.stderrBuf
	if sink != nil {
		p.cmd.Stdout = &processLogWriter{
			stream: ProcessStreamStdout,
			buffer: p.stdoutBuf,
			sink:   sink,
			kill:   p.killFromLogWriter,
		}
		p.cmd.Stderr = &processLogWriter{
			stream: ProcessStreamStderr,
			buffer: p.stderrBuf,
			sink:   sink,
			kill:   p.killFromLogWriter,
		}
	}

	preloadExecutable(p.cmd)
	err := p.cmd.Start()
	if err != nil {
		return -1, err
	}

	p.pid = p.cmd.Process.Pid
	p.markStarted()

	if wait {
		p.waitForExit()
		if p.waitErr != nil {
			return p.pid, p.waitErr
		}
	} else {
		go p.waitForExit()
	}

	return p.pid, nil
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
	if p.cmd.ProcessState != nil && p.cmd.ProcessState.Exited() {
		return
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

		p.mu.Lock()
		defer p.mu.Unlock()
		defer close(p.waitDone)

		p.waitErr = err
		if p.cmd.ProcessState != nil {
			p.exitCode = p.cmd.ProcessState.ExitCode()
			return
		}

		if err != nil {
			if strings.Contains(err.Error(), "wait") && p.cmd.ProcessState != nil {
				p.exitCode = p.cmd.ProcessState.ExitCode()
				return
			}
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
