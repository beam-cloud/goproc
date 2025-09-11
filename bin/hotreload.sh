#!/bin/bash

BUILD_COMMAND="go build -o bin/goproc cmd/main.go"
BUILD_BINARY_PATH="bin/goproc"

./bin/air --build.cmd "$BUILD_COMMAND" --build.bin $BUILD_BINARY_PATH