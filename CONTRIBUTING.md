# Contributing to marunage

Thank you for your interest in contributing! This guide walks you through setting up a development environment and submitting changes.

## Prerequisites

| Tool | Version | Notes |
|------|---------|-------|
| Go | 1.25.5+ | [https://go.dev/dl/](https://go.dev/dl/) |
| golangci-lint | v2.12.1 | See installation options below |

### Installing golangci-lint v2.12.1

**Option 1 — official install script (recommended, pins to exact version):**

```sh
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh \
  | sh -s -- -b $(go env GOPATH)/bin v2.12.1
```

**Option 2 — Homebrew (latest version; pin if needed):**

```sh
brew install golangci-lint
# To pin to v2.12.1:
# brew pin golangci-lint
```

Verify the installation:

```sh
golangci-lint --version
```

## Development Setup

```sh
git clone https://github.com/haruotsu/marunage.git
cd marunage
go mod download
make build
```

The binary is written to `bin/marunage`.

## Running Tests

```sh
make test
```

This runs `go test ./...` across all packages.

## Linting

```sh
make lint
```

This runs `golangci-lint run ./...`. If `golangci-lint` is not on your `PATH`, the command exits with an error and prints the install URL.

The lint configuration lives in `.golangci.yml` at the project root:

- **version: "2"** — uses the golangci-lint v2 configuration format.
- **linters.default: standard** — enables the standard linter set.
- **errcheck.exclude-functions** — suppresses `errcheck` warnings for `fmt.Fprint*` write errors that are safe to ignore in CLI output.

## Code Style

Format all Go code with `gofmt` before committing:

```sh
make fmt        # format in place
make fmt-check  # verify formatting (non-zero exit if diff found)
```

CI runs `make fmt-check`, so unformatted code will fail the pipeline.

## Submitting Changes

1. **Fork** the repository and create a feature branch from `main`:
   ```sh
   git checkout -b feat/your-feature-name
   ```
2. Make your changes and ensure all checks pass:
   ```sh
   make fmt-check
   make vet
   make lint
   make test
   ```
3. Commit with a clear message explaining *what* and *why*.
4. Open a **Pull Request** against `main`.

### PR Description Requirements

- Describe what the change does and why it is needed.
- List any manual testing steps or edge cases verified.
- Reference related issues with `Fixes #<issue>` or `Closes #<issue>` when applicable.
