#!/usr/bin/env bash
# Pipeline test-stage script (called by a CodeArts "Run Shell command" step):
#   sh .codearts/test.sh unit   unit tests (e2etest excluded, -short mode)
#   sh .codearts/test.sh e2e    e2e tests (e2etest/route-server Ginkgo suite;
#                               the suite builds its own binaries and runs an
#                               embedded etcd, no external dependencies)
# Stage ordering, gating and retry belong to the pipeline product; this script
# only runs one test scope and exits non-zero on failure.
set -e
SCOPE="${1:-unit}"
echo "CI-SMOKE-MARKER v2: webhook trigger test"

# Go toolchain bootstrap (same as .codearts/build.sh)
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
export CGO_ENABLED=0
if [ -d repo_workspace ]; then cd repo_workspace; fi

if command -v go >/dev/null 2>&1 && [ "$(go env GOVERSION | cut -d. -f2)" -ge 25 ] 2>/dev/null; then
    echo "use preinstalled go: $(go version)"
else
    case "$(uname -m)" in
        aarch64) HOST_ARCH=arm64 ;;
        *)       HOST_ARCH=amd64 ;;
    esac
    curl -fsSL -o /tmp/go.tgz "https://golang.google.cn/dl/go1.25.0.linux-${HOST_ARCH}.tar.gz"
    mkdir -p "$HOME/.local"
    tar -C "$HOME/.local" -xzf /tmp/go.tgz
    export PATH="$HOME/.local/go/bin:$PATH"
fi
go version

if [ "${SCOPE}" = "e2e" ]; then
    echo "running e2e tests (this may take tens of minutes) ..."
    go test ./e2etest/route-server -count=1 -timeout 60m
else
    echo "running unit tests ..."
    go test $(go list ./... | grep -v '/e2etest') -short -count=1
fi
echo "tests passed: ${SCOPE}"
