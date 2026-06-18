package goproc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/beam-cloud/goproc/proto"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
)

const processLogAckTimeout = 30 * time.Second

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

func (cs *GoProcServer) StreamExec(stream proto.GoProc_StreamExecServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}

	execReq := first.GetExec()
	if execReq == nil {
		return stream.Send(&proto.StreamExecResponse{
			Message: &proto.StreamExecResponse_Exited{
				Exited: &proto.ExecProcessExited{
					Pid:      -1,
					ExitCode: -1,
					ErrorMsg: "first stream exec message must be an exec request",
				},
			},
		})
	}

	proc, err := NewProcess(stream.Context())
	if err != nil {
		return stream.Send(&proto.StreamExecResponse{
			Message: &proto.StreamExecResponse_Exited{
				Exited: &proto.ExecProcessExited{Pid: -1, ExitCode: -1, ErrorMsg: err.Error()},
			},
		})
	}

	session := newStreamExecSession(stream)
	go session.readAcks()
	go func() {
		<-stream.Context().Done()
		session.fail(stream.Context().Err())
	}()

	pid, err := proc.ExecWithLogSink(execReq.Args, execReq.Cwd, execReq.Env, false, session)
	if err != nil {
		session.fail(err)
		return session.send(&proto.StreamExecResponse{
			Message: &proto.StreamExecResponse_Exited{
				Exited: &proto.ExecProcessExited{Pid: int32(pid), ExitCode: -1, ErrorMsg: err.Error()},
			},
		})
	}

	cs.processMap.Store(pid, proc)
	session.setPID(int32(pid))

	if err := session.send(&proto.StreamExecResponse{
		Message: &proto.StreamExecResponse_Started{
			Started: &proto.ExecProcessStarted{Pid: int32(pid)},
		},
	}); err != nil {
		session.fail(err)
		proc.killFromLogWriter()
		return nil
	}
	session.markStarted()

	exitCode, waitErr := proc.Wait()
	if session.err() != nil {
		return nil
	}

	errorMsg := ""
	if waitErr != nil {
		errorMsg = waitErr.Error()
	}
	return session.send(&proto.StreamExecResponse{
		Message: &proto.StreamExecResponse_Exited{
			Exited: &proto.ExecProcessExited{
				Pid:      int32(pid),
				ExitCode: int32(exitCode),
				ErrorMsg: errorMsg,
			},
		},
	})
}

type streamExecSession struct {
	stream proto.GoProc_StreamExecServer

	sendMu sync.Mutex
	seq    atomic.Uint64

	pendingMu sync.Mutex
	pending   map[uint64]chan *proto.ProcessLogAck

	started     chan struct{}
	startedOnce sync.Once
	done        chan struct{}
	doneOnce    sync.Once
	errMu       sync.Mutex
	failErr     error
	pid         atomic.Int32
}

func newStreamExecSession(stream proto.GoProc_StreamExecServer) *streamExecSession {
	return &streamExecSession{
		stream:  stream,
		pending: map[uint64]chan *proto.ProcessLogAck{},
		started: make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (s *streamExecSession) setPID(pid int32) {
	s.pid.Store(pid)
}

func (s *streamExecSession) markStarted() {
	s.startedOnce.Do(func() {
		close(s.started)
	})
}

func (s *streamExecSession) send(resp *proto.StreamExecResponse) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.Send(resp)
}

func (s *streamExecSession) fail(err error) {
	if err == nil {
		return
	}
	s.errMu.Lock()
	if s.failErr == nil {
		s.failErr = err
	}
	s.errMu.Unlock()

	s.doneOnce.Do(func() {
		close(s.done)
	})
}

func (s *streamExecSession) err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.failErr
}

func (s *streamExecSession) readAcks() {
	for {
		req, err := s.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.fail(io.EOF)
			} else {
				s.fail(err)
			}
			return
		}

		ack := req.GetAck()
		if ack == nil {
			s.fail(errors.New("stream exec stream received non-ack message after exec request"))
			return
		}

		s.pendingMu.Lock()
		ch := s.pending[ack.Seq]
		s.pendingMu.Unlock()
		if ch == nil {
			continue
		}

		select {
		case ch <- ack:
		case <-s.done:
			return
		}
	}
}

func (s *streamExecSession) WriteProcessLog(streamName string, data []byte) error {
	select {
	case <-s.started:
	case <-s.done:
		return firstNonNilError(s.err(), errors.New("stream exec stream closed before process start was acknowledged"))
	case <-s.stream.Context().Done():
		return s.stream.Context().Err()
	}

	seq := s.seq.Add(1)
	ackCh := make(chan *proto.ProcessLogAck, 1)
	s.pendingMu.Lock()
	s.pending[seq] = ackCh
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, seq)
		s.pendingMu.Unlock()
	}()

	if err := s.send(&proto.StreamExecResponse{
		Message: &proto.StreamExecResponse_Chunk{
			Chunk: &proto.ProcessLogChunk{
				Pid:    s.pid.Load(),
				Stream: streamName,
				Seq:    seq,
				Data:   data,
			},
		},
	}); err != nil {
		s.fail(err)
		return err
	}

	timer := time.NewTimer(processLogAckTimeout)
	defer timer.Stop()

	select {
	case ack := <-ackCh:
		if ack.Ok {
			return nil
		}
		err := fmt.Errorf("process log seq %d rejected: %s", seq, ack.ErrorMsg)
		s.fail(err)
		return err
	case <-timer.C:
		err := fmt.Errorf("timed out waiting for process log ack seq %d", seq)
		s.fail(err)
		return err
	case <-s.done:
		return firstNonNilError(s.err(), errors.New("stream exec stream closed"))
	case <-s.stream.Context().Done():
		err := s.stream.Context().Err()
		s.fail(err)
		return err
	}
}

func firstNonNilError(err error, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
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

func (cs *GoProcServer) Ready(ctx context.Context, req *proto.ReadyRequest) (*proto.ReadyResponse, error) {
	return &proto.ReadyResponse{Ok: true}, nil
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
