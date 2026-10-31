# Build Guide

[English](build.md) | [简体中文](build.zh-CN.md)

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
5. **Packages** everything into `output/DistributedScheduler-<version>-<timestamp>.zip`, ready for deployment

After the build completes, you can find the deployable zip at:

```
output/DistributedScheduler-<version>-<timestamp>.zip
```

For deployment instructions, see the [Deployment Guide](./deploy.md).

---

## Make Targets

| Target | Description |
|--------|-------------|
| `build` | Compile both `route-server` and `config_generator` |
| `build-binary` | Compile `route-server` only |
| `build-config-tool` | Compile `config_generator` only |
| `package` | Build + copy deploy assets + generate `version.info` + create `.zip` archive |
| `test` | Run all tests, log to `build/test.log` |
| `test-coverage` | Run all tests with coverage, report to `build/coverage.out` |
| `test-unit` | Run all packages with `-short` (currently includes E2E), log to `build/unit-test.log` |
| `clean` | Remove `build/`, `output/`, and loose `.zip` files |
| `all` | `clean` + `build` |
| `fmt` | `go fmt ./...` |
| `vet` | `go vet ./...` |
| `lint` | `fmt` + `vet` |

> `make test`, `make test-unit` and `make test-coverage` currently pipe test output through `tee`
> without preserving the `go test` exit status. A successful Make exit status does not prove that tests
> passed. Use `go test` directly for a reliable result, as shown below. The E2E suite does not check
> `testing.Short()`, so `-short` does not currently exclude it.

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
├── DistributedScheduler/
│   └── bin/route-server                 # Server binary
└── config_generator                     # Config tool
```

Version injection: when building inside a git repository, the `route-server` binary has metadata embedded via ldflags:

| Field | Source |
|-------|--------|
| `version` | `git describe --tags --always --dirty` |
| `tagOrBranch` | `git rev-parse --abbrev-ref HEAD` |
| `commitId` | `git rev-parse HEAD` |
| `buildTime` | UTC timestamp (`YYYYMMDDhhmmss`) |

If git is not available, these fields default to `"unknown"`.

### build-binary

Build only `route-server`. Useful for faster feedback during server development.

```bash
make build-binary
```

Output: `output/DistributedScheduler/bin/route-server`

### build-config-tool

Build only `config_generator`. Useful when working on config generation logic.

```bash
make build-config-tool
```

Output: `output/config_generator`

### package

Build both binaries, copy deploy assets, generate `version.info`, and create a `.zip` archive.

```bash
make package
```

Output: `output/DistributedScheduler-<version>-<timestamp>.zip`

The `version.info` file under `output/DistributedScheduler/` records the build metadata (read from git at build time):

```
Build Tag/Branch: <branch>
Build Commit ID: <commit>
Build Time: <timestamp>
```

Package contents:

```
<package-root>/
├── config_generator
├── deploy.sh
├── *.yaml                              # All YAML templates from deploy/
├── scale/
├── pipelines/
└── DistributedScheduler/
    ├── bin/route-server                # Server binary
    └── version.info                    # Build metadata
```

`deploy.sh` expects exactly this layout: it looks for the runnable package under
`<package-root>/DistributedScheduler/` and for `config_generator`, `*.yaml`, `scale/`, and
`pipelines/` next to itself.

### CodeArts build

The Huawei Cloud CodeArts build task runs [`.codearts/build.sh`](../.codearts/build.sh) and publishes
`route-server-<version>-<timestamp>-linux-<arch>.tar.gz`. Its inner layout is identical to the
`make package` output above (assets at the archive root, runnable package under
`DistributedScheduler/`), so the same `deploy.sh` workflow applies after `tar -xzf`.

---

## Usage

The recommended workflow for developing and releasing RouteServer:

```bash
# 1. Format and check code quality
make lint

# 2. Test a specific package for quick feedback (example)
go test ./pkg/common/scheduler/... -v

# 3. Run full test suite before committing
go test ./... -v -timeout 60m

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
