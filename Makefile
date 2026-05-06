SHELL := /bin/bash

BIN_DIR := bin
BIN := $(BIN_DIR)/marunage
PKG := github.com/haruotsu/marunage
CMD_PKG := ./cmd/marunage

# Inject git describe as version when available; fall back to "dev".
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint fmt fmt-check vet tidy clean web-install web-dev web-build web-lint build-nextjs build-all

# Single-quote the -ldflags value so shell metacharacters that may appear in an
# unexpected git tag name (backticks, dollar-paren command substitution, etc.)
# reach the linker verbatim instead of being evaluated by the shell.
build:
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

# Build Go binary with embedded Next.js static export.
build-nextjs: web-build
	@mkdir -p $(BIN_DIR)
	go build -tags nextjs -ldflags '-X $(PKG)/internal/version.version=$(VERSION)' -o $(BIN) $(CMD_PKG)

# Full build: web assets + Go binary (without embed).
build-all: web-build build
