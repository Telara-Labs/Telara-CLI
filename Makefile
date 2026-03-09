.PHONY: build test lint release-dry-run

BIN_NAME = telara
LDFLAGS = -X gitlab.com/teleraai/telara-cli/services/cli/internal/version.Version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev) \
          -X gitlab.com/teleraai/telara-cli/services/cli/internal/version.Commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo none) \
          -X gitlab.com/teleraai/telara-cli/services/cli/internal/version.Date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BIN_NAME) .

test:
	go test ./...

lint:
	golangci-lint run ./...

release-dry-run:
	goreleaser release --snapshot --clean
