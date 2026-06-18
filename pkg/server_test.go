package goproc

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/beam-cloud/goproc/proto"
	"google.golang.org/grpc"
)

func TestStreamExecWaitRequestSendsStartedBeforeOutputAck(t *testing.T) {
	server := &GoProcServer{}
	stream := newFakeStreamExecServerStream()
	defer stream.cancel()

	wait := true
	stream.recv <- &proto.StreamExecRequest{
		Message: &proto.StreamExecRequest_Exec{
			Exec: &proto.ExecProcessRequest{
				Args: []string{"sh", "-c", "printf hello"},
				Cwd:  "/",
				Wait: &wait,
			},
		},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.StreamExec(stream)
	}()

	started := recvStreamExecResponse(t, stream).GetStarted()
	if started == nil || started.Pid <= 0 {
		t.Fatalf("expected started event with pid, got %#v", started)
	}

	chunk := recvStreamExecResponse(t, stream).GetChunk()
	if chunk == nil {
		t.Fatal("expected process log chunk")
	}
	if got, want := string(chunk.Data), "hello"; got != want {
		t.Fatalf("unexpected chunk data: got %q want %q", got, want)
	}

	stream.recv <- &proto.StreamExecRequest{
		Message: &proto.StreamExecRequest_Ack{
			Ack: &proto.ProcessLogAck{Seq: chunk.Seq, Ok: true},
		},
	}

	exited := recvStreamExecResponse(t, stream).GetExited()
	if exited == nil {
		t.Fatal("expected process exited event")
	}
	if got, want := exited.ExitCode, int32(0); got != want {
		t.Fatalf("unexpected exit code: got %d want %d", got, want)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stream exec to return")
	}
}

func TestReady(t *testing.T) {
	resp, err := (&GoProcServer{}).Ready(context.Background(), &proto.ReadyRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Ok {
		t.Fatal("expected ready response")
	}
}

func TestExecDetachedProcessOutlivesRPCContext(t *testing.T) {
	server := &GoProcServer{}
	ctx, cancel := context.WithCancel(context.Background())

	wait := false
	resp, err := server.Exec(ctx, &proto.ExecProcessRequest{
		Args: []string{"sh", "-c", "sleep 5"},
		Cwd:  "/",
		Wait: &wait,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Ok {
		t.Fatalf("exec failed: %s", resp.ErrorMsg)
	}
	cancel()
	time.Sleep(100 * time.Millisecond)

	proc, err := server.getProcess(resp.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if !proc.Running() {
		t.Fatal("detached process was killed when RPC context was canceled")
	}
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
}

func TestExecWaitProcessUsesRPCContext(t *testing.T) {
	server := &GoProcServer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wait := true
	done := make(chan *proto.ExecProcessResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := server.Exec(ctx, &proto.ExecProcessRequest{
			Args: []string{"sh", "-c", "sleep 5"},
			Cwd:  "/",
			Wait: &wait,
		})
		if err != nil {
			errCh <- err
			return
		}
		done <- resp
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		t.Fatal(err)
	case resp := <-done:
		if resp.Ok {
			t.Fatal("expected canceled wait exec to fail")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for wait exec cancellation")
	}
}

func TestWatchTCPListenerReportsConsecutiveFailures(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := watchTCPListener(ctx, listener.Addr().String(), 10*time.Millisecond, 5*time.Millisecond, 10*time.Millisecond, 2)
	select {
	case err := <-done:
		t.Fatalf("watchdog failed while listener was healthy: %v", err)
	case <-time.After(40 * time.Millisecond):
	}

	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	<-acceptDone

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected listener failure")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for listener failure")
	}
}

func TestWatchTCPListenerReportsInitialFailuresWithoutWaitingForInterval(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := watchTCPListener(ctx, address, time.Hour, 5*time.Millisecond, 10*time.Millisecond, 2)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected listener failure")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("watchdog waited for periodic interval before reporting initial failures")
	}
}

func TestStreamExecCancelAfterStartDoesNotKillProcess(t *testing.T) {
	server := &GoProcServer{}
	stream := newFakeStreamExecServerStream()

	wait := false
	stream.recv <- &proto.StreamExecRequest{
		Message: &proto.StreamExecRequest_Exec{
			Exec: &proto.ExecProcessRequest{
				Args: []string{"sh", "-c", "sleep 5"},
				Cwd:  "/",
				Wait: &wait,
			},
		},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.StreamExec(stream)
	}()

	started := recvStreamExecResponse(t, stream).GetStarted()
	if started == nil || started.Pid <= 0 {
		t.Fatalf("expected started event with pid, got %#v", started)
	}

	stream.cancel()
	time.Sleep(100 * time.Millisecond)

	proc, err := server.getProcess(started.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if !proc.Running() {
		t.Fatal("process was killed when stream was canceled")
	}
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stream exec to return after explicit kill")
	}
}

func recvStreamExecResponse(t *testing.T, stream *fakeStreamExecServerStream) *proto.StreamExecResponse {
	t.Helper()

	select {
	case resp := <-stream.sent:
		return resp
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stream exec response")
		return nil
	}
}

type fakeStreamExecServerStream struct {
	grpc.ServerStream
	ctx    context.Context
	cancel context.CancelFunc
	recv   chan *proto.StreamExecRequest
	sent   chan *proto.StreamExecResponse
}

func newFakeStreamExecServerStream() *fakeStreamExecServerStream {
	ctx, cancel := context.WithCancel(context.Background())
	return &fakeStreamExecServerStream{
		ctx:    ctx,
		cancel: cancel,
		recv:   make(chan *proto.StreamExecRequest, 4),
		sent:   make(chan *proto.StreamExecResponse, 4),
	}
}

func (s *fakeStreamExecServerStream) Context() context.Context {
	return s.ctx
}

func (s *fakeStreamExecServerStream) Send(resp *proto.StreamExecResponse) error {
	select {
	case s.sent <- resp:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func (s *fakeStreamExecServerStream) Recv() (*proto.StreamExecRequest, error) {
	select {
	case req, ok := <-s.recv:
		if !ok {
			return nil, io.EOF
		}
		if req == nil {
			return nil, errors.New("nil stream exec request")
		}
		return req, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}
