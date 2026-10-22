#!/usr/bin/env bash
set -ex

BUILD_COMMIT_ID=""
BUILD_BRANCH=""

if [ -z "${CB_TRIGGER_MR_SOURCE_BRANCH}" ]; then
    BUILD_COMMIT_ID=${CID_REPO_COMMIT_LONG}
    BUILD_BRANCH=${CID_REPO_BRANCH}
    echo "This is a DEPLOY pipeline, trigger: ${BUILD_TRIGGER_USER}"
    echo "BUILD_COMMIT_ID: ${CID_REPO_COMMIT_LONG}"
    echo "BUILD_BRANCH: ${BUILD_BRANCH}, REPO_URL:${CID_REPO_URL}"

    # version info
    {
      echo "Build Tag/Branch: ${BUILD_BRANCH}"
      echo "Build Commit ID: ${BUILD_COMMIT_ID}"
      echo "Build Time: ${CID_BUILD_TIME}"
    } >> version.info
else
    # 待合入的源commit id，branch
    BUILD_COMMIT_ID=${CB_TRIGGER_MR_LAST_COMMIT}
    BUILD_BRANCH=${CB_TRIGGER_MR_SOURCE_BRANCH}
    echo "This is a MR pipeline, trigger: ${BUILD_TRIGGER_USER}"
    echo "BUILD_COMMIT_ID: ${BUILD_COMMIT_ID}"
    echo "BUILD_BRANCH: ${BUILD_BRANCH}, SOURCE_REPO_URL:${CB_TRIGGER_MR_SOURCE_REPO}"
    echo "TARGET_COMMIT_ID: ${CB_TRIGGER_MR_TARGET_COMMIT}"
    echo "TARGET_BRANCH_ID: ${CID_REPO_BRANCH}, TARGET_REPO_URL:${CID_REPO_URL}"

    {
      echo "Build Tag/Branch: ${BUILD_BRANCH}"
      echo "Build Commit ID: ${BUILD_COMMIT_ID}"
      echo "Build Time: ${CID_BUILD_TIME}"
      echo "Source Repo Url: ${CB_TRIGGER_MR_SOURCE_REPO}"
      echo "Target Repo Url: ${CID_REPO_URL}"
    } >> version.info
fi

name="model-orch"
ROOT_PATH=$(cd "$(dirname "$0")"/..; pwd)
PROJECT_PATH=${ROOT_PATH}

prepare_go_mod(){
  #set go mod env
  export GOOS=linux
  export GONOPROXY=''''
  export GO111MODULE=on
  export GOGOARCH=arm64
  export GOPROXY=https://cmc.centralrepo.rnd.huawei.com/artifactory/go-central-repo/,https://go.cloudartifact.dgg.dragon.tools.huawei.com/artifactory/cbu-go-common/,https://cmc.centralrepo.rnd.huawei.com/artifactory/product_go/,direct
  export GONOSUMDB=*
  export GOSUMDB=off
}

init_buildenv(){
  export GOPATH=$PROJECT_PATH/../..
  mkdir -p $GOPATH/src
  rm -rf $GOPATH/src/DistributedScheduler
  cp -r $PROJECT_PATH $GOPATH/src/DistributedScheduler
}

build_model_orch_binary(){
    echo "start build DistributedScheduler binary"

    pushd "$PROJECT_PATH/cmd/route_server"
    goldflags="-s -linkmode=external -extldflags '-Wl,-z,now' \
      -X 'infer-dp-scheduler/cmd/route_server/server.version=${PKG_VERSION}'\
      -X 'infer-dp-scheduler/cmd/route_server/server.tagOrBranch=${BUILD_BRANCH}'\
      -X 'infer-dp-scheduler/cmd/route_server/server.commitId=${BUILD_COMMIT_ID}'\
      -X 'infer-dp-scheduler/cmd/route_server/server.buildTime=${CID_BUILD_TIME}'"
    go clean --modcache
    go mod tidy
    CGO_CFLAGS="-D_FORTIFY_SOURCE=2 -O2 -fstack-protector-strong" go build -o ${name} -buildmode=pie -ldflags "${goldflags}"
    popd

    pushd "$PROJECT_PATH/cmd/config-tool"
    CGO_CFLAGS="-D_FORTIFY_SOURCE=2 -O2 -fstack-protector-strong" go build -o config_generator -buildmode=pie -ldflags "${goldflags}"
    popd

    echo "end build DistributedScheduler binary"
}

prepare_go_mod
build_model_orch_binary

mkdir -p output/DistributedScheduler/bin
cp "$PROJECT_PATH"/cmd/route_server/model-orch output/DistributedScheduler/bin/
mv version.info output/DistributedScheduler/
cp "$PROJECT_PATH"/Dockerfile output/
cp "$PROJECT_PATH"/cmd/config-tool/config_generator output/
cp -r "$PROJECT_PATH"/deploy/* output/
cd output
# make package
zip -q -r "${PKG_NAME}"-"${PKG_VERSION}"-"${CID_BUILD_TIME}".zip ./
mv "${PKG_NAME}"-"${PKG_VERSION}"-"${CID_BUILD_TIME}".zip ../
cd ../
rm -rf output
