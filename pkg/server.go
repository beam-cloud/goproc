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
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/beam-cloud/goproc/proto"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
)

const (
	processLogAckTimeout       = 30 * time.Second
	listenerWatchdogInterval   = 2 * time.Second
	listenerWatchdogTimeout    = 200 * time.Millisecond
	listenerWatchdogFirstRetry = 50 * time.Millisecond
	listenerWatchdogMaxFailure = 2
	// SIGWINCH is ignored by default, so workers can send it safely to older goproc builds.
	listenerRestartSignal = syscall.SIGWINCH
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
	if ctx == nil {
		ctx = context.Background()
	}

	addr := fmt.Sprintf(":%d", port)

	terminationChan := make(chan os.Signal, 1)
	signal.Notify(terminationChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(terminationChan)

	restartChan := make(chan os.Signal, 1)
	signal.Notify(restartChan, listenerRestartSignal)
	defer signal.Stop(restartChan)

	reaperCtx, stopReaper := context.WithCancel(ctx)
	defer stopReaper()
	go reapOrphans(reaperCtx, func(pid int) bool {
		_, ok := cs.processMap.Load(pid)
		return ok
	})

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

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

		serveDone := make(chan error, 1)
		go func() {
			serveDone <- s.Serve(localListener)
		}()

		watchCtx, stopWatch := context.WithCancel(ctx)
		watchdogDone := cs.watchListener(watchCtx, port)

		select {
		case <-ctx.Done():
			stopWatch()
			s.GracefulStop()
			return ctx.Err()
		case sig := <-terminationChan:
			log.Info().Msgf("Termination signal (%v) received. Shutting down server...", sig)
			stopWatch()
			s.GracefulStop()
			return nil
		case <-restartChan:
			stopWatch()
			_ = localListener.Close()

			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Info().Msg("Listener restart signal received. Rebinding server...")
		case err := <-serveDone:
			stopWatch()
			_ = localListener.Close()
			s.Stop()

			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				log.Warn().Err(err).Msg("gRPC server stopped unexpectedly; restarting")
			} else {
				log.Warn().Msg("gRPC server stopped unexpectedly; restarting")
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		case err := <-watchdogDone:
			stopWatch()
			_ = localListener.Close()
			// After gVisor restore the old listener can be unreachable without
			// waking grpc's accept loop. Do not call Stop here; rebind a fresh
			// listener and let the stale server goroutine die if the runtime
			// eventually unblocks it.

			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Warn().Err(err).Msg("gRPC listener health check failed; restarting")

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
}

func (cs *GoProcServer) watchListener(ctx context.Context, port uint) <-chan error {
	address := net.JoinHostPort("127.0.0.1", strconv.FormatUint(uint64(port), 10))
	return watchTCPListener(ctx, address, listenerWatchdogInterval, listenerWatchdogFirstRetry, listenerWatchdogTimeout, listenerWatchdogMaxFailure)
}

func watchTCPListener(ctx context.Context, address string, interval, firstRetry, timeout time.Duration, maxFailures int) <-chan error {
	done := make(chan error, 1)

	go func() {
		if maxFailures < 1 {
			maxFailures = 1
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		failures := 0
		var retryTimer *time.Timer
		var retry <-chan time.Time

		stopRetry := func() {
			if retryTimer == nil {
				return
			}
			if !retryTimer.Stop() {
				select {
				case <-retryTimer.C:
				default:
				}
			}
			retryTimer = nil
			retry = nil
		}
		defer stopRetry()

		scheduleRetry := func() {
			if retryTimer != nil {
				return
			}
			retryTimer = time.NewTimer(firstRetry)
			retry = retryTimer.C
		}

		probe := func() bool {
			conn, err := net.DialTimeout("tcp", address, timeout)
			if err == nil {
				_ = conn.Close()
				failures = 0
				stopRetry()
				return false
			}

			failures++
			if failures < maxFailures {
				scheduleRetry()
				return false
			}

			select {
			case done <- err:
			case <-ctx.Done():
			}
			return true
		}

		if probe() {
			return
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-retry:
				retryTimer = nil
				retry = nil
				if probe() {
					return
				}
			case <-ticker.C:
				if probe() {
					return
				}
			}
		}
	}()

	return done
}

func (cs *GoProcServer) Exec(ctx context.Context, req *proto.ExecProcessRequest) (*proto.ExecProcessResponse, error) {
	wait := false
	if req.Wait != nil {
		wait = *req.Wait
	}

	processCtx := context.Background()
	if wait {
		processCtx = ctx
	}

	proc, err := NewProcess(processCtx)
	if err != nil {
		return &proto.ExecProcessResponse{
			Ok:       false,
			ErrorMsg: err.Error(),
		}, nil
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

	proc, err := NewProcess(context.Background())
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
