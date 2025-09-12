package goproc

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
)

type GoProcServer struct {
	cfg GoProcConfig
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
		// grpc.WriteBufferSize(writeBufferSizeBytes),
		grpc.NumStreamWorkers(uint32(runtime.NumCPU())),
	)
	// proto.RegisterGoProcServer(s, cs)

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
