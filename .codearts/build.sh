#!/usr/bin/env bash
# Called by the "Run Shell command" step of the Huawei Cloud CodeArts build task:
#   sh .codearts/build.sh
# Available environment variables:
#   GOARCH  Target architecture, defaults to host architecture; set to arm64 for Ascend aarch64 cross-compilation
set -e

export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
export CGO_ENABLED=0

# CodeArts build machine checks out code into the working directory; tolerate subdirectory layout
if [ -d repo_workspace ]; then cd repo_workspace; fi

TARGET_ARCH="${GOARCH:-}"
if [ -z "${TARGET_ARCH}" ]; then
    case "$(uname -m)" in
        x86_64)  TARGET_ARCH=amd64 ;;
        aarch64) TARGET_ARCH=arm64 ;;
        *)       TARGET_ARCH=amd64 ;;
    esac
fi
[ "${TARGET_ARCH}" = "$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')" ] || export GOARCH="${TARGET_ARCH}"

# Go toolchain: use the preinstalled version if >=1.25, otherwise install a fixed version from the official mirror
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

# Inject version info via ldflags
VERSION=$(git describe --tags --always 2>/dev/null || git rev-parse --short HEAD 2>/dev/null || echo dev)
BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)
COMMIT=$(git rev-parse HEAD 2>/dev/null || echo unknown)
BUILD_TIME=$(date -u '+%Y-%m-%d_%H%M%S')
LDFLAGS="-X 'infer-dp-scheduler/cmd/route_server/server.version=${VERSION}' -X 'infer-dp-scheduler/cmd/route_server/server.tagOrBranch=${BRANCH}' -X 'infer-dp-scheduler/cmd/route_server/server.commitId=${COMMIT}' -X 'infer-dp-scheduler/cmd/route_server/server.buildTime=${BUILD_TIME}'"

echo "building route-server ${VERSION} (${BRANCH} ${COMMIT}) for linux/${TARGET_ARCH} ..."
mkdir -p output/route-server/route-server/bin
go build -trimpath -ldflags "${LDFLAGS}" -o output/route-server/route-server/bin/route-server ./cmd/route_server
go build -trimpath -o output/route-server/config_generator ./cmd/config-tool

# Assemble the deployment package
cp deploy/deploy.sh output/route-server/
cp deploy/*.yaml output/route-server/
cp -r deploy/k8s-configmap output/route-server/
cp -r deploy/scale output/route-server/
cp -r deploy/pipelines output/route-server/

PKG="route-server-${VERSION}-${BUILD_TIME}-linux-${TARGET_ARCH}.tar.gz"
tar -czf "${PKG}" -C output .
echo "package created: ${PKG}"
ls -lh "${PKG}"
