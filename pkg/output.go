package goproc

import (
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

var errDrainStopped = errors.New("output drain stopped")

// outputDrain copies one of a process's output pipes to dst. os/exec closes its
// own pipes when WaitDelay expires, so a background child that inherited one
// fails its next write. A stopped drain keeps reading and discards instead.
type outputDrain struct {
	r       *os.File
	dst     io.Writer
	mu      sync.Mutex
	stopped atomic.Bool
}

func (d *outputDrain) run(result chan<- error) {
	defer d.r.Close()
	_, err := io.Copy(d, d.r)
	result <- err
	if d.stopped.Load() {
		_, _ = io.Copy(io.Discard, d.r)
	}
}

func (d *outputDrain) Write(b []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped.Load() {
		return 0, errDrainStopped
	}
	return d.dst.Write(b)
}

// stopDrains returns once any write in progress is done. Nothing more is written.
func stopDrains(drains []*outputDrain) {
	for _, d := range drains {
		// Set outside mu, or the next write could take it first.
		d.stopped.Store(true)
	}
	for _, d := range drains {
		d.mu.Lock()
		d.dst = nil
		d.mu.Unlock()
	}
}
