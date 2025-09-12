package goproc

import (
	"context"
	"fmt"

	"github.com/beam-cloud/goproc/proto"
	"google.golang.org/grpc"
)

type GoProcClient struct {
	ctx    context.Context
	port   uint
	conn   *grpc.ClientConn
	client proto.GoProcClient
}

func NewGoProcClient(ctx context.Context, port uint) (*GoProcClient, error) {
	c := &GoProcClient{
		ctx:  ctx,
		port: port,
	}

	conn, err := grpc.NewClient(fmt.Sprintf("0.0.0.0:%d", port), grpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	c.conn = conn
	c.client = proto.NewGoProcClient(conn)
	return c, nil
}

func (c *GoProcClient) Cleanup() error {
	if c.conn != nil {
		return c.conn.Close()
	}

	return nil
}
