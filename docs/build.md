# Build Guide

This document describes how to build RouteServer from source code using the provided `Makefile`.

## Prerequisites

Ensure the following tools are installed on your build machine:

| Tool | Minimum Version | Purpose                          |
|------|-----------------|----------------------------------|
| Go   | 1.25.0          | Compile Go source code           |
| Make | 3.82            | Build orchestration              |

> **Operating System**: The build process supports Linux, macOS, and Windows (WSL). The deployment package (`deploy.sh`) targets Linux environments.

Verify the installations:

```bash
go version
make --version
```

---

## Quick Start

Clone the repository and build everything in one command:

```bash
git clone <repo-url> && cd <repo-dir>
make clean && make package
```

This single command performs the following steps:

1. **Cleans** all previous build artifacts (`build/` and `output/` directories)
2. **Builds** the main server binary (`route-server`) from `cmd/route_server/`
3. **Builds** the config generation tool (`config_generator`) from `cmd/config-tool/`
4. **Copies** deployment scripts, YAML templates, auto-scaling scripts, and pipeline configurations into the package directory
5. **Packages** everything into `output/route-server-<version>-<timestamp>.zip`, ready for deployment

After the build completes, you can find the deployable zip at:

```
output/route-server-<version>-<timestamp>.zip
```

For deployment instructions, see the [Deployment Guide](./deploy.md).

---

## Make Targets

| Target | Description |
|--------|-------------|
| `build` | Compile both `route-server` and `config_generator` |
| `build-binary` | Compile `route-server` only |
| `build-config-tool` | Compile `config_generator` only |
| `package` | Build + copy deploy assets + create `.zip` archive |
| `test` | Run all tests, log to `build/test.log` |
| `test-coverage` | Run all tests with coverage, report to `build/coverage.out` |
| `test-unit` | Run unit tests only (`-short`), log to `build/unit-test.log` |
| `clean` | Remove `build/`, `output/`, and loose `.zip` files |
| `all` | `clean` + `build` |
| `fmt` | `go fmt ./...` |
| `vet` | `go vet ./...` |
| `lint` | `fmt` + `vet` |

### build

Compile both executables:

- **`route-server`** — Main server binary, entry point `cmd/route_server/`
- **`config_generator`** — Config generation tool, entry point `cmd/config-tool/`

```bash
make build
```

Output:

```
output/
└── route-server/
    ├── route-server/bin/route-server       # Server binary
    └── config_generator                    # Config tool
```

Version injection: when building inside a git repository, the `route-server` binary has metadata embedded via ldflags:

| Field | Source |
|-------|--------|
| `version` | `git describe --tags --always --dirty` |
| `tagOrBranch` | `git rev-parse --abbrev-ref HEAD` |
| `commitId` | `git rev-parse HEAD` |
| `buildTime` | UTC timestamp |

If git is not available, these fields default to `"unknown"`.

### build-binary

Build only `route-server`. Useful for faster feedback during server development.

```bash
make build-binary
```

Output: `output/route-server/route-server/bin/route-server`

### build-config-tool

Build only `config_generator`. Useful when working on config generation logic.

```bash
make build-config-tool
```

Output: `output/route-server/config_generator`

### package

Build both binaries, copy deploy assets, and create a `.zip` archive.

```bash
make package
```

Output: `output/route-server-<version>-<timestamp>.zip`

Package contents:

```
route-server/
├── route-server/bin/route-server
├── config_generator
├── deploy.sh
├── *.yaml                              # All YAML templates from deploy/
├── scale/
└── pipelines/
```

---

## Usage

The recommended workflow for developing and releasing RouteServer:

```bash
# 1. Format and check code quality
make lint

# 2. Run unit tests for quick feedback
make test-unit

# 3. Run full test suite before committing
make test

# 4. Build and package for deployment
make clean && make package
```

---

## Troubleshooting

| Issue | Likely Cause | Solution |
|-------|--------------|----------|
| `go: command not found` | Go is not installed | Install Go 1.25.0 or higher |
| `make: command not found` | Make is not installed | Install make 3.82+ via your package manager |
| `fatal: not a git repository` | Build is running outside a git clone | Clone the repository first, or version fields will default to `"unknown"` |
| `fatal: path '...': permission denied` | File permission issue | Check file ownership; ensure you have read/write access to the project directory |
| `Package zip is empty` | Binaries failed to build silently | Run `make build` separately first and check the output for errors |
| `undefined: ...` / `cannot find package` | Missing or outdated dependencies | Run `go mod tidy` to sync `go.sum`, then retry `make build` |
| The build succeeds but `route-server` crashes at startup | Mismatched config file format | Verify the config matches the version of the binary; check the log output for parsing errors |
| `output/` directory is missing after build | Build was skipped due to an earlier error | Check terminal output for error messages; run `make clean && make build` to isolate the step |
