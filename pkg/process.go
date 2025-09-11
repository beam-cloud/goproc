package goproc

import (
	"context"
)

type Process struct {
	ctx context.Context
}

func NewProcess(ctx context.Context) (*Process, error) {
	return &Process{ctx: ctx}, nil
}

func (p *Process) Start() {
	return
}
