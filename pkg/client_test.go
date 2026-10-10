package goproc

import (
	"context"
	"github.com/beam-cloud/goproc/proto"
	"google.golang.org/grpc"
	"net"
	"strings"
	"testing"
)

type largeOutputServer struct {
	proto.UnimplementedGoProcServer
	output string
}

func (s largeOutputServer) Stdout(context.Context, *proto.StdoutProcessRequest) (*proto.StdoutProcessResponse, error) {
	return &proto.StdoutProcessResponse{Ok: true, Stdout: s.output}, nil
}
func (s largeOutputServer) Stderr(context.Context, *proto.StderrProcessRequest) (*proto.StderrProcessResponse, error) {
	return &proto.StderrProcessResponse{Ok: true, Stderr: s.output}, nil
}
func TestClientReadsOutputLargerThanDefaultGRPCLimit(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	output := strings.Repeat("x", 5<<20)
	proto.RegisterGoProcServer(server, largeOutputServer{output: output})
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	client, err := NewGoProcClient(context.Background(), "127.0.0.1", uint(listener.Addr().(*net.TCPAddr).Port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Cleanup() })
	for name, read := range map[string]func(int) (string, error){"stdout": client.Stdout, "stderr": client.Stderr} {
		result, err := read(1)
		if err != nil || result != output {
			t.Fatalf("%s: bytes=%d err=%v", name, len(result), err)
		}
	}
}
