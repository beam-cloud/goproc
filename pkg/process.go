package goproc

import (
	"context"
	"os"
	"os/exec"
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
		Logger.Errorf("Failed to execute command: %v", err)
	}

	return nil
}
