package goproc

import (
	"context"
)

type GoProcClient struct {
	ctx context.Context
}

func NewGoProcClient(ctx context.Context, cfg GoProcConfig) (*GoProcClient, error) {
	c := &GoProcClient{
		ctx: ctx,
	}

	return c, nil
}

func (c *GoProcClient) Cleanup() error {
	return nil
}
