# MonitorAll Go 后端构建脚本
#
# 常用目标：
#   make deps     下载依赖
#   make build    编译单二进制（CGO_ENABLED=0，embed 前端产物）
#   make run      编译并运行
#   make test     单元测试 + 覆盖率
#   make vet      静态检查
#   make smoke    冒烟自测（建源→取样例→建卡→订阅→收帧→保存）
#   make cert     仅生成自签证书
#   make build-web 构建前端产物到 internal/web/dist（需 Node 环境）
#   make docker   构建 Docker 镜像
#   make clean    清理产物

BINARY      := monitorall
CMD_DIR     := ./cmd/monitorall
OUT_DIR     := ./bin
WEB_DIR     := ../web
DIST_DIR    := ./internal/web/dist
GO          := go
# Go 构建会产生较大的中间产物，若系统 /tmp 很小（tmpfs 仅几十 MB 的机器上很常见）
# 会失败并报 "no space left on device"。这里默认用用户缓存目录并自动创建，
# 需要自定义时用环境变量覆盖即可： GOTMPDIR=/mnt/bigdisk/tmp make build
# ⚠️ 不要写成某个具体的绝对路径——那样换台机器构建就会立刻报
#    "creating work dir: stat <路径>: no such file or directory"。
GOTMPDIR    ?= $(HOME)/.cache/monitorall/tmp

# 单二进制多平台分发必须关闭 CGO（modernc.org/sqlite 为纯 Go 实现）
export CGO_ENABLED := 0
export GOTMPDIR
export GOFLAGS := -mod=mod

.PHONY: all deps build run test vet fmt smoke cert build-web docker clean help

## gotmpdir: 确保 Go 临时目录存在（多数目标的 order-only 前置）
gotmpdir:
	@mkdir -p "$(GOTMPDIR)"

all: deps vet test build

## deps: 下载并整理依赖
deps:
	$(GO) mod download
	$(GO) mod tidy

## build: 编译（embed 前端产物，产物在 bin/monitorall）
build: | gotmpdir
	@mkdir -p $(OUT_DIR)
	$(GO) build -trimpath -ldflags "-s -w" -o $(OUT_DIR)/$(BINARY) $(CMD_DIR)
	@echo "构建完成: $(OUT_DIR)/$(BINARY)"

## run: 编译并运行（读取 ./config.yaml，缺省使用内置默认值）
run: build
	$(OUT_DIR)/$(BINARY)

## test: 单元测试 + 覆盖率
test: | gotmpdir
	$(GO) test ./... -count=1 -coverprofile=$(OUT_DIR)/coverage.out
	$(GO) tool cover -func=$(OUT_DIR)/coverage.out | tail -1

## vet: 静态检查（go vet）
vet: | gotmpdir
	$(GO) vet ./...

## fmt: 格式化
fmt:
	$(GO) fmt ./...

## cert: 仅生成自签证书（含 LAN IP SAN）
cert: build
	MONITORALL_SERVER_TLSENABLED=true $(OUT_DIR)/$(BINARY) --version >/dev/null 2>&1 || true
	@echo "证书在 data/cert/ 下：ca.crt / server.crt / server.key"

## build-web: 构建前端并拷贝产物到 internal/web/dist（供 go:embed）
build-web:
	cd $(WEB_DIR) && npm install && npm run build
	mkdir -p $(DIST_DIR)
	cp -r $(WEB_DIR)/dist/. $(DIST_DIR)/
	@echo "前端产物已拷贝到 $(DIST_DIR)"

## smoke: 冒烟自测脚本
smoke: build
	bash ../scripts/smoke.sh

## docker: 构建 Docker 镜像
docker:
	docker build -f ../deploy/Dockerfile -t monitorall:local ..

## clean: 清理产物
clean:
	rm -rf $(OUT_DIR)
	rm -f $(BINARY)

## help: 显示帮助
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //'
