to exec a process:

  grpcurl -plaintext \
    -import-path ./proto \
    -proto goproc.proto \
    -d '{"args": ["ls", "-l"], "cwd": "/tmp", "env": ["FOO=bar"]}' \
    localhost:50051 \
    goproc.GoProc/Exec