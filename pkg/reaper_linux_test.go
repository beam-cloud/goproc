package goproc

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReapOrphansWaitsForReparentedChildren(t *testing.T) {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	// sh exits at once; its background sleeps are re-parented to us and exit unwaited.
	if err := exec.Command("sh", "-c", "sleep 0.1 & sleep 0.1 & exit 0").Run(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if len(zombieChildren(os.Getpid())) < 2 {
		t.Fatalf("expected zombies before the reaper runs, got %v", zombieChildren(os.Getpid()))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reapOrphans(ctx, func(int) bool { return false })
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if len(zombieChildren(os.Getpid())) == 0 {
			return
		}
	}
	t.Fatalf("zombies left: %v", zombieChildren(os.Getpid()))
}
