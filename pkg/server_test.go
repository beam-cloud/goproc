package goproc

import (
	"context"
	"errors"
	"io"
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
