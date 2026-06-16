package goproc

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

type logChunk struct {
	stream string
	data   []byte
}

type blockingLogSink struct {
	chunks   chan logChunk
	acks     chan struct{}
	failures chan struct{}
	err      error
}

func newBlockingLogSink() *blockingLogSink {
	return &blockingLogSink{
		chunks:   make(chan logChunk, 8),
		acks:     make(chan struct{}, 8),
		failures: make(chan struct{}, 8),
	}
}

func (s *blockingLogSink) WriteProcessLog(stream string, data []byte) error {
	if s.err != nil {
		s.failures <- struct{}{}
		return s.err
	}

	s.chunks <- logChunk{stream: stream, data: append([]byte(nil), data...)}
	select {
	case <-s.acks:
		return nil
	case <-time.After(time.Second):
		return errors.New("timed out waiting for test ack")
	}
}

func TestExecWithLogSinkExposesStdoutOnlyAfterAck(t *testing.T) {
	sink := newBlockingLogSink()
	proc, err := NewProcess(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	pid, err := proc.ExecWithLogSink([]string{"sh", "-c", "printf hello"}, "/", nil, false, sink)
	if err != nil {
		t.Fatal(err)
	}
	if pid <= 0 {
		t.Fatalf("expected process pid, got %d", pid)
	}

	chunk := <-sink.chunks
	if chunk.stream != ProcessStreamStdout {
		t.Fatalf("unexpected stream: got %q want %q", chunk.stream, ProcessStreamStdout)
	}
	if !bytes.Equal(chunk.data, []byte("hello")) {
		t.Fatalf("unexpected chunk: got %q want %q", chunk.data, "hello")
	}
	if stdout := proc.Stdout(); stdout != "" {
		t.Fatalf("stdout was exposed before log ack: %q", stdout)
	}

	sink.acks <- struct{}{}
	exitCode, err := proc.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("unexpected exit code: got %d want 0", exitCode)
	}
	if stdout := proc.Stdout(); stdout != "hello" {
		t.Fatalf("unexpected stdout after ack: got %q want %q", stdout, "hello")
	}
}

func TestExecWithLogSinkCapturesStdoutAndStderrStreams(t *testing.T) {
	sink := newBlockingLogSink()
	proc, err := NewProcess(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	_, err = proc.ExecWithLogSink([]string{"sh", "-c", "printf out; printf err >&2"}, "/", nil, false, sink)
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]string{}
	for len(seen) < 2 {
		select {
		case chunk := <-sink.chunks:
			seen[chunk.stream] += string(chunk.data)
			sink.acks <- struct{}{}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for log chunks; saw %#v", seen)
		}
	}

	exitCode, err := proc.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("unexpected exit code: got %d want 0", exitCode)
	}
	if got := proc.Stdout(); got != "out" {
		t.Fatalf("unexpected stdout: got %q want %q", got, "out")
	}
	if got := proc.Stderr(); got != "err" {
		t.Fatalf("unexpected stderr: got %q want %q", got, "err")
	}
}

func TestExecWithLogSinkTerminatesProcessOnLogFailure(t *testing.T) {
	sink := newBlockingLogSink()
	sink.err = errors.New("log unavailable")

	proc, err := NewProcess(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	_, err = proc.ExecWithLogSink([]string{"sh", "-c", "printf hello; sleep 5"}, "/", nil, false, sink)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-sink.failures:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for process log write failure")
	}

	done := make(chan struct{})
	go func() {
		_, _ = proc.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("process did not terminate after log failure")
	}
	if stdout := proc.Stdout(); stdout != "" {
		t.Fatalf("stdout was exposed after log failure: %q", stdout)
	}
}
