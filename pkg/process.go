package goproc

import (
	"context"
	"os"
	"os/exec"

	"github.com/rs/zerolog/log"
)

type Process struct {
	ctx context.Context
}

func NewProcess(ctx context.Context) (*Process, error) {
	return &Process{ctx: ctx}, nil
}

func (p *Process) Exec() error {
	cmd := exec.CommandContext(p.ctx, "ls", "-l")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Start()
	if err != nil {
		log.Error().Err(err).Msg("Failed to execute command")
	}

	return nil
}
