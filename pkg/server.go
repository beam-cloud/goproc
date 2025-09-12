package goproc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"

	"github.com/beam-cloud/goproc/proto"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
)

type GoProcServer struct {
	cfg GoProcConfig
	proto.UnimplementedGoProcServer
	processMap sync.Map
}

func NewGoProcServer(cfg GoProcConfig) (*GoProcServer, error) {
	return &GoProcServer{cfg: cfg}, nil
}

func (cs *GoProcServer) StartServer(ctx context.Context, port uint) error {
	addr := fmt.Sprintf(":%d", port)

	localListener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error().Err(err).Msgf("Failed to listen on %s", addr)
		return err
	}

	maxMessageSize := cs.cfg.GRPCMessageSizeBytes
	s := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMessageSize),
		grpc.MaxSendMsgSize(maxMessageSize),
		grpc.NumStreamWorkers(uint32(runtime.NumCPU())),
	)
	proto.RegisterGoProcServer(s, cs)

	log.Info().Msgf("Running @%s, cfg: %+v", addr, cs.cfg)

	go s.Serve(localListener)

	// Block until a termination signal is received
	terminationChan := make(chan os.Signal, 1)
	signal.Notify(terminationChan, os.Interrupt, syscall.SIGTERM)

	sig := <-terminationChan
	log.Info().Msgf("Termination signal (%v) received. Shutting down server...", sig)

	s.GracefulStop()
	return nil
}

func (cs *GoProcServer) Exec(ctx context.Context, req *proto.ExecProcessRequest) (*proto.ExecProcessResponse, error) {
	proc, err := NewProcess(ctx)
	if err != nil {
		return &proto.ExecProcessResponse{
			Ok:       false,
			ErrorMsg: err.Error(),
		}, nil
	}

	wait := false
	if req.Wait != nil {
		wait = *req.Wait
	}

	pid, err := proc.Exec(req.Args, req.Cwd, req.Env, wait)
	if err != nil {
		return &proto.ExecProcessResponse{
			Ok:       false,
			ErrorMsg: err.Error(),
		}, nil
	}

	cs.processMap.Store(pid, proc)

	return &proto.ExecProcessResponse{
		Ok:       true,
		Pid:      int32(pid),
		ErrorMsg: "",
	}, nil
}

func (cs *GoProcServer) Wait(ctx context.Context, req *proto.WaitProcessRequest) (*proto.WaitProcessResponse, error) {
	proc, err := cs.getProcess(req.Pid)
	if err != nil {
		return &proto.WaitProcessResponse{
			Ok:       false,
			ErrorMsg: err.Error(),
		}, nil
	}

	exitCode, err := proc.Wait()
	if err != nil {
		return &proto.WaitProcessResponse{
			Ok:       false,
			ErrorMsg: err.Error(),
		}, nil
	}

	return &proto.WaitProcessResponse{
		Ok:       true,
		ExitCode: int32(exitCode),
	}, nil
}

func (cs *GoProcServer) Kill(ctx context.Context, req *proto.KillProcessRequest) (*proto.KillProcessResponse, error) {
	proc, err := cs.getProcess(req.Pid)
	if err != nil {
		return &proto.KillProcessResponse{
			Ok:       false,
			ErrorMsg: err.Error(),
		}, nil
	}

	err = proc.Kill()
	if err != nil {
		return &proto.KillProcessResponse{
			Ok:       false,
			ErrorMsg: err.Error(),
		}, nil
	}

	return &proto.KillProcessResponse{
		Ok:       true,
		ErrorMsg: "",
	}, nil
}

func (cs *GoProcServer) Signal(ctx context.Context, req *proto.SignalProcessRequest) (*proto.SignalProcessResponse, error) {
	proc, err := cs.getProcess(req.Pid)
	if err != nil {
		return &proto.SignalProcessResponse{
			Ok:       false,
			ErrorMsg: err.Error(),
		}, nil
	}

	err = proc.Signal(syscall.Signal(req.Signal))
	if err != nil {
		return &proto.SignalProcessResponse{
			Ok:       false,
			ErrorMsg: err.Error(),
		}, nil
	}

	return &proto.SignalProcessResponse{
		Ok:       true,
		ErrorMsg: "",
	}, nil
}

func (cs *GoProcServer) Status(ctx context.Context, req *proto.StatusProcessRequest) (*proto.StatusProcessResponse, error) {
	proc, err := cs.getProcess(req.Pid)
	if err != nil {
		return &proto.StatusProcessResponse{
			Ok:       false,
			ErrorMsg: err.Error(),
		}, nil
	}

	return &proto.StatusProcessResponse{
		Ok:       true,
		ErrorMsg: "",
		Process: &proto.ProcessInfo{
			Pid:      int32(proc.pid),
			Cmd:      proc.cmd.String(),
			Cwd:      proc.cmd.Dir,
			Env:      proc.cmd.Env,
			Running:  proc.Running(),
			ExitCode: int32(proc.ExitCode()),
		},
	}, nil
}

func (cs *GoProcServer) Stdout(ctx context.Context, req *proto.StdoutProcessRequest) (*proto.StdoutProcessResponse, error) {
	proc, err := cs.getProcess(req.Pid)
	if err != nil {
		return &proto.StdoutProcessResponse{
			Ok:       false,
			ErrorMsg: err.Error(),
			Stdout:   "",
		}, nil
	}

	return &proto.StdoutProcessResponse{
		Ok:       true,
		ErrorMsg: "",
		Stdout:   proc.Stdout(),
	}, nil
}

func (cs *GoProcServer) Stderr(ctx context.Context, req *proto.StderrProcessRequest) (*proto.StderrProcessResponse, error) {
	proc, err := cs.getProcess(req.Pid)
	if err != nil {
		return &proto.StderrProcessResponse{
			Ok:       false,
			ErrorMsg: err.Error(),
			Stderr:   "",
		}, nil
	}

	return &proto.StderrProcessResponse{
		Ok:       true,
		ErrorMsg: "",
		Stderr:   proc.Stderr(),
	}, nil
}

func (cs *GoProcServer) ListProcesses(ctx context.Context, req *proto.ListProcessesRequest) (*proto.ListProcessesResponse, error) {
	processes, err := cs.listProcesses()
	if err != nil {
		return &proto.ListProcessesResponse{
			Ok:       false,
			ErrorMsg: err.Error(),
		}, nil
	}

	return &proto.ListProcessesResponse{
		Ok:        true,
		ErrorMsg:  "",
		Processes: processes,
	}, nil

}

func (cs *GoProcServer) listProcesses() ([]*proto.ProcessInfo, error) {
	processes := make([]*proto.ProcessInfo, 0)

	cs.processMap.Range(func(key, value interface{}) bool {
		processes = append(processes, &proto.ProcessInfo{
			Pid:      int32(key.(int)),
			Cmd:      value.(*Process).cmd.String(),
			Cwd:      value.(*Process).cmd.Dir,
			Env:      value.(*Process).cmd.Env,
			Running:  value.(*Process).Running(),
			ExitCode: int32(value.(*Process).ExitCode()),
		})
		return true
	})

	return processes, nil
}

func (cs *GoProcServer) getProcess(pid int32) (*Process, error) {
	procIface, ok := cs.processMap.Load(int(pid))
	if !ok {
		return nil, errors.New("process not found")
	}

	proc, ok := procIface.(*Process)
	if !ok {
		return nil, errors.New("process not found")
	}

	return proc, nil
}
