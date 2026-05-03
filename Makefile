SHELL := /bin/bash

BIN_DIR := bin
BIN := $(BIN_DIR)/marunage
PKG := github.com/haruotsu/marunage
CMD_PKG := ./cmd/marunage

# Inject git describe as version when available; fall back to "dev".
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X $(PKG)/internal/version.version=$(VERSION)

.PHONY: build test lint fmt fmt-check vet tidy clean

build:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN) $(CMD_PKG)

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
