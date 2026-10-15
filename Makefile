VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/egrif/runnr/internal/cli.version=$(VERSION)

.PHONY: build install test lint ui

build:
	go build -ldflags "$(LDFLAGS)" -o bin/runnr ./cmd/runnr

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/runnr

test:
	go test -race ./...

lint:
	gofmt -l . | (! grep .) && go vet ./...

ui: build
	./bin/runnr ui --open
