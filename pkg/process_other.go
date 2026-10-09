//go:build !linux

package goproc

func (p *Process) start() error {
	return p.cmd.Start()
}
