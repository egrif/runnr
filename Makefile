VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/egrif/runnr/internal/cli.version=$(VERSION)

.PHONY: build install test test-postgres lint ui

build:
	go build -ldflags "$(LDFLAGS)" -o bin/runnr ./cmd/runnr

install: build
	go install -ldflags "$(LDFLAGS)" ./cmd/runnr

test:
	go test -race ./...

lint:
	gofmt -l . | (! grep .) && go vet ./...

ui: build
	./bin/runnr ui --open

# Runs the store tests against a throwaway Postgres container too.
PG_TEST_PASS ?= throwaway
test-postgres:
	@port=$$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'); \
	docker run -d --rm --name runnr-pg-test -e POSTGRES_PASSWORD=$(PG_TEST_PASS) -e POSTGRES_DB=runnr -p 127.0.0.1:$$port:5432 postgres:16-alpine >/dev/null && \
	until docker exec runnr-pg-test pg_isready -h 127.0.0.1 -U postgres -d runnr >/dev/null 2>&1; do sleep 1; done; \
	RUNNR_TEST_POSTGRES="postgres://postgres:$(PG_TEST_PASS)@127.0.0.1:$$port/runnr" go test -count=1 ./internal/store/; \
	status=$$?; docker rm -f runnr-pg-test >/dev/null; exit $$status
