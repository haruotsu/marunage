SHELL := /bin/bash

BIN_DIR := bin
BIN := $(BIN_DIR)/marunage
PKG := github.com/haruotsu/marunage
CMD_PKG := ./cmd/marunage

# Inject git describe as version when available; fall back to "dev".
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build build-go test lint fmt fmt-check vet tidy clean web-install web-dev web-build web-lint build-nextjs build-all dev serve

# Default build: includes the Next.js web UI (requires Node.js 22+).
# Use `make build-go` when you only want the Go binary without assets (no web UI).
build: build-nextjs

# Go-only build without web UI. Used internally by CI lint/test steps.
build-go:
	@mkdir -p $(BIN_DIR)
	go build -ldflags '-X $(PKG)/internal/version.version=$(VERSION)' -o $(BIN) $(CMD_PKG)

test:
	go test ./...

lint:
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "golangci-lint not installed; see https://golangci-lint.run/welcome/install/"; \
		exit 1; \
	fi
	golangci-lint run ./...

fmt:
	gofmt -w .

fmt-check:
	@diff=$$(gofmt -l .); \
	if [ -n "$$diff" ]; then \
		echo "gofmt differences in:"; echo "$$diff"; exit 1; \
	fi

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf $(BIN_DIR)

web-install:
	cd web && npm ci

web-dev:
	cd web && npm run dev

web-build:
	cd web && npm run build

web-lint:
	cd web && npm run lint

# Build Go binary with embedded Next.js static export (recommended for production).
# Copies web/out to internal/web/out so //go:embed out is reachable without ".." traversal.
build-nextjs: web-install web-build
	@mkdir -p $(BIN_DIR)
	rm -rf internal/web/out && cp -r web/out internal/web/out
	go build -tags nextjs -ldflags '-X $(PKG)/internal/version.version=$(VERSION)' -o $(BIN) $(CMD_PKG)

# Alias kept for compatibility.
build-all: build-nextjs

# Start the Next.js dev server (hot-reload).
# In another terminal run `marunage web` (or `make serve`) for the Go API.
dev: web-install
	cd web && npm run dev

# Build with web UI and immediately start `marunage web`.
serve: build-nextjs
	$(BIN) web
