package goproc

import (
	"bytes"
	"context"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// reapOrphans waits for processes re-parented to goproc, which is PID 1 in a
// container and a child subreaper elsewhere. A killed process tree or an
// exiting daemon would otherwise leave zombies for the life of the sandbox.
// A zombie is reaped once it survives a sweep: goproc's own children are
// waited for by their Process the moment they exit, so one still there on the
// next pass was re-parented.
func reapOrphans(ctx context.Context, tracked func(pid int) bool) {
	_ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGCHLD)
	defer signal.Stop(sigs)
	seen := map[int]bool{}
	again := time.After(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-sigs:
			if again == nil {
				again = time.After(100 * time.Millisecond)
			}
			continue
		case <-again:
		}
		again = nil
		zombies := zombieChildren(os.Getpid())
		next := make(map[int]bool, len(zombies))
		for _, pid := range zombies {
			if seen[pid] && !tracked(pid) {
				_, _ = syscall.Wait4(pid, nil, syscall.WNOHANG, nil)
				continue
			}
			next[pid] = true
		}
		if seen = next; len(seen) > 0 {
			again = time.After(100 * time.Millisecond)
		}
	}
}

// zombieChildren lists parent's children that exited and were not waited for.
func zombieChildren(parent int) []int {
	entries, _ := os.ReadDir("/proc")
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		fields := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
		if len(fields) > 1 && fields[0] == "Z" && fields[1] == strconv.Itoa(parent) {
			pids = append(pids, pid)
		}
	}
	return pids
}
