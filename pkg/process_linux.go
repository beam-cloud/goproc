//go:build linux

package goproc

import (
	"fmt"
	"os"
)

func (p *Process) start() error {
	// The manager owns the policy; an exec's environment cannot override it.
	cgroupPath := os.Getenv("GOPROC_WORKLOAD_CGROUP")
	if cgroupPath == "" {
		return p.cmd.Start()
	}

	cgroup, err := os.Open(cgroupPath)
	if err != nil {
		return fmt.Errorf("open workload cgroup: %w", err)
	}
	defer cgroup.Close()

	// Place the child before it runs, avoiding an allocation-before-move race.
	p.cmd.SysProcAttr.UseCgroupFD = true
	p.cmd.SysProcAttr.CgroupFD = int(cgroup.Fd())
	return p.cmd.Start()
}
