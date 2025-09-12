package goproc

import (
	"context"
	"fmt"

	"github.com/beam-cloud/goproc/proto"
	"google.golang.org/grpc"
)

type GoProcClient struct {
	ctx    context.Context
	addr   string
	port   uint
	conn   *grpc.ClientConn
	client proto.GoProcClient
}

func NewGoProcClient(ctx context.Context, addr string, port uint) (*GoProcClient, error) {
	c := &GoProcClient{
		ctx:  ctx,
		addr: addr,
		port: port,
	}

	conn, err := grpc.NewClient(fmt.Sprintf("%s:%d", addr, port), grpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	c.conn = conn
	c.client = proto.NewGoProcClient(conn)
	return c, nil
}

func (c *GoProcClient) Exec(args []string, cwd string, env []string, wait bool) (int, error) {
	resp, err := c.client.Exec(c.ctx, &proto.ExecProcessRequest{
		Args: args,
		Cwd:  cwd,
		Env:  env,
		Wait: &wait,
	})
	if err != nil {
		return -1, err
	}
	if !resp.Ok {
		return -1, fmt.Errorf(resp.ErrorMsg)
	}

	return int(resp.Pid), nil
}

func (c *GoProcClient) Wait(pid int) (int, error) {
	resp, err := c.client.Wait(c.ctx, &proto.WaitProcessRequest{
		Pid: int32(pid),
	})
	if err != nil {
		return -1, err
	}
	if !resp.Ok {
		return -1, fmt.Errorf(resp.ErrorMsg)
	}

	return int(resp.ExitCode), nil
}

func (c *GoProcClient) Kill(pid int) error {
	resp, err := c.client.Kill(c.ctx, &proto.KillProcessRequest{
		Pid: int32(pid),
	})
	if err != nil {
		return err
	}
	if !resp.Ok {
		return fmt.Errorf(resp.ErrorMsg)
	}

	return nil
}

func (c *GoProcClient) Signal(pid int, signal int) error {
	resp, err := c.client.Signal(c.ctx, &proto.SignalProcessRequest{
		Pid:    int32(pid),
		Signal: int32(signal),
	})
	if err != nil {
		return err
	}
	if !resp.Ok {
		return fmt.Errorf(resp.ErrorMsg)
	}

	return nil
}

func (c *GoProcClient) Status(pid int) (int, error) {
	resp, err := c.client.Status(c.ctx, &proto.StatusProcessRequest{
		Pid: int32(pid),
	})
	if err != nil {
		return -1, err
	}
	if !resp.Ok {
		return -1, fmt.Errorf(resp.ErrorMsg)
	}

	return int(resp.Process.ExitCode), nil
}

func (c *GoProcClient) Stdout(pid int) (string, error) {
	resp, err := c.client.Stdout(c.ctx, &proto.StdoutProcessRequest{
		Pid: int32(pid),
	})
	if err != nil {
		return "", err
	}
	if !resp.Ok {
		return "", fmt.Errorf(resp.ErrorMsg)
	}

	return resp.Stdout, nil
}

func (c *GoProcClient) Stderr(pid int) (string, error) {
	resp, err := c.client.Stderr(c.ctx, &proto.StderrProcessRequest{
		Pid: int32(pid),
	})
	if err != nil {
		return "", err
	}
	if !resp.Ok {
		return "", fmt.Errorf(resp.ErrorMsg)
	}

	return resp.Stderr, nil
}

func (c *GoProcClient) ListProcesses(args []string, cwd string, env []string) ([]*proto.ProcessInfo, error) {
	resp, err := c.client.ListProcesses(c.ctx, &proto.ListProcessesRequest{
		Args: args,
		Cwd:  cwd,
		Env:  env,
	})
	if err != nil {
		return nil, err
	}
	if !resp.Ok {
		return nil, fmt.Errorf(resp.ErrorMsg)
	}

	return resp.Processes, nil
}

func (c *GoProcClient) Cleanup() error {
	if c.conn != nil {
		return c.conn.Close()
	}

	return nil
}
