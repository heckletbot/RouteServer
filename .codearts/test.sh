#!/usr/bin/env bash
# Pipeline test-stage script (called by a CodeArts "Run Shell command" step):
#   sh .codearts/test.sh          unit tests, then e2e tests (default)
#   sh .codearts/test.sh all      same as no argument
#   sh .codearts/test.sh unit     unit tests only (e2etest excluded, -short mode)
#   sh .codearts/test.sh e2e [N]  e2e tests only, split across N Ginkgo
#                                 processes (default 4)
#
# The e2e scope runs e2etest/route-server (Ginkgo suite; the suite builds its
# own binaries and runs an embedded etcd, no external dependencies) via the
# ginkgo CLI so specs are split across parallel processes; plain `go test`
# would run the whole suite serially.
#
# Stage ordering, gating and retry belong to the pipeline product; this script
# only runs one test scope and exits non-zero on failure.
set -e
SCOPE="${1:-all}"
E2E_PROCS="${2:-4}"

case "${SCOPE}" in
    all|unit|e2e) ;;
    *)
        echo "unknown scope: ${SCOPE} (expected all | unit | e2e [N])" >&2
        exit 2
        ;;
esac
case "${E2E_PROCS}" in
    *[!0-9]*|'0')
        echo "invalid e2e concurrency: ${E2E_PROCS} (expected a positive integer)" >&2
        exit 2
        ;;
esac

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

run_unit() {
    echo "running unit tests ..."
    go test $(go list ./... | grep -v '/e2etest') -short -count=1
}

run_e2e() {
    echo "running e2e tests with ${E2E_PROCS} parallel processes (this may take tens of minutes) ..."
    go run github.com/onsi/ginkgo/v2/ginkgo -procs="${E2E_PROCS}" -timeout 60m ./e2etest/route-server
}

case "${SCOPE}" in
    unit) run_unit ;;
    e2e)  run_e2e ;;
    all)  run_unit; run_e2e ;;
esac
echo "tests passed: ${SCOPE}"
