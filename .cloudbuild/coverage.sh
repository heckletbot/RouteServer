go version
export GO111MODULE=on
export GOPROXY=https://cmc.centralrepo.rnd.huawei.com/artifactory/go-central-repo/,https://go.cloudartifact.dgg.dragon.tools.huawei.com/artifactory/cbu-go-common/,https://cmc.centralrepo.rnd.huawei.com/artifactory/product_go/,https://go.cloudartifact.dgg.dragon.tools.huawei.com/artifactory/cbu-go-public,https://cmc.centralrepo.rnd.huawei.com/cbu-go,http://mirrors.tools.huawei.com/goproxy,direct
export GONOSUMDB=*
export GOSUMB=off

# 配置工具路径（关键：动态获取 GOPATH）
export GOPATH=$(go env GOPATH)
export PATH=$GOPATH/bin:$PATH:/usr/local/go/bin
  
# 安装 gocov gocov-xml gocov-html 别删
go mod tidy
#gocov版本与go版本密切相关,建议先社区查询版本兼容性https://pkg.go.dev/github.com 
# go1.20版本gocov使用v1.1.0
go install github.com/axw/gocov/gocov@v1.1.0
go install github.com/AlekSi/gocov-xml@latest
go install github.com/matm/gocov-html/cmd/gocov-html@latest
go install github.com/jstemmer/go-junit-report@latest
go install github.com/alexec/junit2html@latest
export PATH=$PATH:/root/go/bin/:/usr/local/go/bin

# 进入项目根目录（DistributedScheduler/）
cd $(dirname "$0")/../ || {
  echo "错误：无法进入项目根目录"
  exit 1
}

# 验证模块文件
if [ ! -f "go.mod" ]; then
  echo "错误：未找到 go.mod，当前目录：$(pwd)"
  exit 1
fi

# 准备报告目录（在根目录下创建）
mkdir -p Coverage-Report
echo "mode: set" > begin.out

# 执行测试（路径简化为 ./...，基于根目录）
echo "--> go test DistributedScheduler（当前目录：$(pwd)）"
if ! go test -tags debug ./cmd/config-tool -v -failfast=false -coverprofile=dbops.out -cover > test-output.txt 2>&1; then
  echo "测试失败，日志："
  cat test-output.txt
  exit 1
fi

sed -i '1d' dbops.out  # 删除首行模式

cat begin.out dbops.out > Coverage-Report/dbops.out

cat test-output.txt > Coverage-Report/test-output.txt
  
  # Dt执行生成.out文件并生成UT的xml文件
cat Coverage-Report/test-output.txt | go-junit-report > Coverage-Report/report.xml
  
  #将UT执行的xml文件转变为html
junit2html < Coverage-Report/report.xml > Coverage-Report/report.html
  
  # 全量覆盖率xml
gocov convert Coverage-Report/dbops.out | gocov-xml > Coverage-Report/coverage.xml
  
  # 全量覆盖率html
gocov convert Coverage-Report/dbops.out | gocov-html > Coverage-Report/index.html

cat Coverage-Report/test-output.txt