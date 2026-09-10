BINARY  := mixpost
PKG     := ./cmd/mixpost
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/inovector/mixpost-cli/internal/cli.version=$(VERSION) \
	-X github.com/inovector/mixpost-cli/internal/cli.commit=$(COMMIT) \
	-X github.com/inovector/mixpost-cli/internal/cli.date=$(DATE)

.PHONY: all build install test race cover vet fmt tidy clean snapshot completions

all: vet test build

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) $(PKG)

install:
	go install -trimpath -ldflags '$(LDFLAGS)' $(PKG)

test:
	go test ./...

race:
	go test -race ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

vet:
	go vet ./...

fmt:
	gofmt -l -w .

tidy:
	go mod tidy

clean:
	rm -f $(BINARY) coverage.out
	rm -rf dist completions

# snapshot builds the full release matrix locally without publishing anything.
snapshot:
	goreleaser release --snapshot --clean

completions: build
	mkdir -p completions
	./$(BINARY) completion bash > completions/$(BINARY).bash
	./$(BINARY) completion zsh  > completions/_$(BINARY)
	./$(BINARY) completion fish > completions/$(BINARY).fish
