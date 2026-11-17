package goproc

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// socketPath returns a path short enough for a Unix socket address, which
// t.TempDir does not guarantee.
func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "goproc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "manager.sock")
}

func dialSocket(path string) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}
}

func TestServerListensOnSocketInsteadOfPort(t *testing.T) {
	socket := socketPath(t)

	// A port that is free now. The server is given it and must leave it free.
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	reserved.Close()

	cfg := DefaultGoProcConfig()
	cfg.ServerSocket = socket
	server, err := NewGoProcServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- server.StartServer(ctx, uint(port)) }()

	client, err := NewGoProcClientWithDialer(context.Background(), dialSocket(socket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Cleanup() })
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if err = client.Ready(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not answer on the socket: %v", err)
		}
	}

	pid, err := client.Exec([]string{"sh", "-c", "exit 7"}, "/", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if code, err := client.Wait(pid); err != nil || code != 7 {
		t.Fatalf("exit code = %d, err = %v, want 7", code, err)
	}

	if conn, err := net.DialTimeout("tcp", reserved.Addr().String(), time.Second); err == nil {
		conn.Close()
		t.Fatalf("server listens on port %d as well as on its socket", port)
	}
	info, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Fatalf("socket mode = %o, want 600", mode)
	}

	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket remains after the server stopped: %v", err)
	}
}

func TestListenReplacesSocketLeftByKilledServer(t *testing.T) {
	socket := socketPath(t)

	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	stale.Close()
	if _, err := net.Listen("unix", socket); err == nil {
		t.Fatal("expected the stale socket to block a plain listen")
	}

	listener, err := listen("unix", socket)
	if err != nil {
		t.Fatalf("listen over a stale socket: %v", err)
	}
	defer listener.Close()
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		t.Fatalf("dial the replaced socket: %v", err)
	}
	conn.Close()
}

func TestWatchListenerReportsRemovedSocket(t *testing.T) {
	socket := socketPath(t)
	listener, err := listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := watchListener(ctx, "unix", socket, 10*time.Millisecond, 5*time.Millisecond, 10*time.Millisecond, 2)
	select {
	case err := <-done:
		t.Fatalf("watchdog failed while the socket was healthy: %v", err)
	case <-time.After(40 * time.Millisecond):
	}

	// The listener is still open, but nothing can reach it any more.
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a listener failure")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the listener failure")
	}
}
