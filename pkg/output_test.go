package goproc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/beam-cloud/goproc/proto"
	"google.golang.org/grpc"
)

// backgroundWriter starts a child that inherits stdout and stderr, outlives the
// drain deadline, and creates marker once several more writes to each worked.
func backgroundWriter(t *testing.T) (args []string, marker string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), "survived")
	script := `(sleep 3; for i in 1 2 3 4 5; do echo late || exit 1; echo late >&2 || exit 1; sleep 0.1; done; : > "$0") & printf now`
	return []string{"sh", "-c", script, marker}, marker
}

func requireFile(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatal("background child could not keep writing to its inherited output")
}

func TestBackgroundChildSurvivesDrainDeadline(t *testing.T) {
	t.Parallel()
	proc, err := NewProcess(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	args, marker := backgroundWriter(t)
	if _, err := proc.Exec(args, "/", os.Environ(), false); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if code, err := proc.Wait(); code != 1 || !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("Wait() = %d, %v; want 1, ErrWaitDelay", code, err)
	}
	if waited := time.Since(started); waited < processWaitDelay-200*time.Millisecond {
		t.Fatalf("gave up on output after %v", waited)
	}
	if got := proc.Stdout(); got != "now" {
		t.Fatalf("stdout=%q; want %q", got, "now")
	}

	requireFile(t, marker)
	if got := proc.Stdout() + proc.Stderr(); got != "" {
		t.Fatalf("kept output %q after the drain deadline", got)
	}
}

type ackingExecStream struct {
	grpc.ServerStream
	ctx      context.Context
	requests chan *proto.StreamExecRequest
	exited   *proto.ExecProcessExited
	returned atomic.Bool
	lateSend atomic.Bool
}

func (s *ackingExecStream) Context() context.Context { return s.ctx }

func (s *ackingExecStream) Recv() (*proto.StreamExecRequest, error) {
	select {
	case req := <-s.requests:
		return req, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

func (s *ackingExecStream) Send(resp *proto.StreamExecResponse) error {
	if s.returned.Load() {
		s.lateSend.Store(true)
	}
	if chunk := resp.GetChunk(); chunk != nil {
		s.requests <- &proto.StreamExecRequest{Message: &proto.StreamExecRequest_Ack{
			Ack: &proto.ProcessLogAck{Seq: chunk.Seq, Ok: true},
		}}
	}
	if exited := resp.GetExited(); exited != nil {
		s.exited = exited
	}
	return nil
}

func TestStreamExecBackgroundChildSurvivesDrainDeadline(t *testing.T) {
	t.Parallel()
	// grpc cancels the stream's context when the handler returns.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	args, marker := backgroundWriter(t)
	stream := &ackingExecStream{ctx: ctx, requests: make(chan *proto.StreamExecRequest, 16)}
	stream.requests <- &proto.StreamExecRequest{Message: &proto.StreamExecRequest_Exec{
		Exec: &proto.ExecProcessRequest{Args: args, Cwd: "/", Env: os.Environ()},
	}}

	err := (&GoProcServer{}).StreamExec(stream)
	stream.returned.Store(true)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if stream.exited == nil || stream.exited.ExitCode != 1 {
		t.Fatalf("exited=%v; want code 1", stream.exited)
	}

	requireFile(t, marker)
	if stream.lateSend.Load() {
		t.Fatal("sent on the stream after its handler returned")
	}
}

type funcLogSink func(stream string, data []byte) error

func (f funcLogSink) WriteProcessLog(stream string, data []byte) error { return f(stream, data) }

func TestLogSinkIsNotUsedAfterWait(t *testing.T) {
	t.Parallel()
	proc, err := NewProcess(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var returned, late atomic.Bool
	firstWrite := filepath.Join(t.TempDir(), "first-write")
	sink := funcLogSink(func(string, []byte) error {
		if returned.Load() {
			late.Store(true)
		}
		if err := os.WriteFile(firstWrite, nil, 0600); err != nil {
			return err
		}
		// Still in progress at the drain deadline.
		time.Sleep(processWaitDelay + 2*time.Second)
		late.CompareAndSwap(false, returned.Load())
		return nil
	})
	script := `echo one; while [ ! -e "$0" ]; do sleep 0.02; done; echo two`
	if _, err := proc.ExecWithLogSink([]string{"sh", "-c", script, firstWrite}, "/", os.Environ(), false, sink); err != nil {
		t.Fatal(err)
	}
	code, err := proc.Wait()
	returned.Store(true)
	if code != 1 || !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("Wait() = %d, %v; want 1, ErrWaitDelay", code, err)
	}
	if got := proc.Stdout(); got != "one\n" {
		t.Fatalf("stdout=%q; want %q", got, "one\n")
	}
	time.Sleep(500 * time.Millisecond)
	if late.Load() {
		t.Fatal("log sink used after Wait returned")
	}
}

func TestLogFailureAfterExitCannotReportStatusSuccess(t *testing.T) {
	t.Parallel()
	proc, err := NewProcess(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	logErr := errors.New("log unavailable")
	sink := funcLogSink(func(string, []byte) error {
		time.Sleep(300 * time.Millisecond)
		return logErr
	})
	if _, err := proc.ExecWithLogSink([]string{"sh", "-c", "printf owned-output"}, "/", os.Environ(), false, sink); err != nil {
		t.Fatal(err)
	}
	if code, err := proc.Wait(); code != 1 || !errors.Is(err, logErr) {
		t.Fatalf("Wait() = %d, %v; want 1, %v", code, err, logErr)
	}
}

func TestLogFailureWaitsForOtherStream(t *testing.T) {
	t.Parallel()
	proc, err := NewProcess(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	logErr := errors.New("log unavailable")
	var writing atomic.Bool
	sink := funcLogSink(func(stream string, _ []byte) error {
		if stream == ProcessStreamStdout {
			time.Sleep(300 * time.Millisecond)
			return logErr
		}
		writing.Store(true)
		defer writing.Store(false)
		time.Sleep(800 * time.Millisecond)
		return nil
	})
	if _, err := proc.ExecWithLogSink([]string{"sh", "-c", "echo out; echo err >&2"}, "/", os.Environ(), false, sink); err != nil {
		t.Fatal(err)
	}
	if code, err := proc.Wait(); code != 1 || !errors.Is(err, logErr) {
		t.Fatalf("Wait() = %d, %v; want 1, %v", code, err, logErr)
	}
	if writing.Load() {
		t.Fatal("log sink still in use after Wait returned")
	}
	if got := proc.Stderr(); got != "err\n" {
		t.Fatalf("stderr=%q; want %q", got, "err\n")
	}
}

func TestExitCodeOutranksDrainDeadline(t *testing.T) {
	t.Parallel()
	proc, err := NewProcess(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proc.Exec([]string{"sh", "-c", "sleep 4 & exit 3"}, "/", os.Environ(), false); err != nil {
		t.Fatal(err)
	}
	defer proc.Kill()
	if code, err := proc.Wait(); code != 3 || err != nil {
		t.Fatalf("Wait() = %d, %v; want 3, nil", code, err)
	}
}
