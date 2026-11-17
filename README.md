On Linux, `GOPROC_WORKLOAD_CGROUP` places exec children in that cgroup before
they run. The manager's environment owns this policy; per-exec environments
cannot override it. The runtime must configure the cgroup and its memory limit.

`GOPROC_SOCKET` makes the manager listen on that Unix socket instead of its
port, so it takes no port of the machine it runs in. Only the socket's owner
can connect. Clients reach it with `NewGoProcClientWithDialer`.

Wait and Status report signal exits as `128 + signal` (SIGKILL is 137), with
stdout/stderr retained. A nonzero exit is process completion, not an RPC error.

to exec a process:

  grpcurl -plaintext \
    -import-path ./proto \
    -proto goproc.proto \
    -d '{"args": ["ls", "-l"], "cwd": "/tmp", "env": ["FOO=bar"]}' \
    localhost:50051 \
    goproc.GoProc/Exec
