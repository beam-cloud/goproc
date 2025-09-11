GOOS ?= linux
GOARCH ?= $(shell uname -m | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/')
imageVersion ?= latest

init:
	cp pkg/config.default.yaml config.yaml

protocol:
	cd proto && ./gen.sh

build:
	docker build --target build --platform=$(GOOS)/$(GOARCH) --tag localhost:5001/goproc:$(imageVersion) .
	docker push localhost:5001/goproc:$(imageVersion)

debug:
	./bin/hotreload.sh