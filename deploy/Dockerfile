# MonitorAll — 多源异构监控看板平台（Go 后端 + 嵌入式 Vue3 前端）
# 多阶段构建：先由 Node 构建前端产物，再打进 Go 单二进制（go:embed）。
#
# ── 基础镜像一律锁定到具体版本（不用 node:22-alpine 这类滚动 tag）──────────────
# 为什么：发布校验依赖「产物哈希一致」，基础镜像漂移会让同一份代码两次构建产物不同，
# 且排查时无法确认当时用的是哪个基础镜像。成本只是几个字符。
# 注释里的 digest 是锁定当日的实际值，供事后复核（tag 是可变的，digest 才是最终凭据）：
#   node:22.11-alpine            sha256:b64ced2e7cd0a4816699fe308ce6e8a08ccba463c757c00c14cd372e3d2c763e
#   golang:1.22.5-bookworm       sha256:af9b40f2b1851be993763b85288f8434af87b5678af04355b1e33ff530b5765f
#   debian:bookworm-20250224-slim sha256:12c396bd585df7ec21d5679bb6a83d4878bc4415ce926c9e5ea6426d23c60bdc
# 注：未直接用 digest 锁定，是为了保留基础镜像的安全更新通道；本项目无自动重建流水线，
#     升级基础镜像是一次人工动作（看到上游 CVE 或半年一次），升级时同步更新上面三行注释。

# ————————————————— 阶段一：构建前端产物 —————————————————
FROM node:22.11-alpine AS webbuilder
WORKDIR /src
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ————————————————— 阶段二：构建 Go 二进制（CGO_ENABLED=0，纯 Go SQLite） —————————————————
FROM golang:1.22.5-bookworm AS builder

ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOPROXY=https://goproxy.cn,direct

# 构建期版本追溯：由外部传入（docker build --build-arg GIT_COMMIT=...）。
# 不写死，也不在镜像内 git rev-parse —— 构建上下文里不一定有 .git（源码包场景），
# 且把 .git 拷进镜像会白白增大体积。缺省 unknown，与 Makefile 里的兜底保持一致。
ARG GIT_COMMIT=unknown

WORKDIR /src

# 先复制依赖清单，利用 Docker 层缓存
COPY server/go.mod server/go.sum ./
RUN go mod download

COPY server/ ./

# 嵌入前端产物
COPY --from=webbuilder /src/dist ./internal/web/dist

RUN go build -trimpath \
    -ldflags "-s -w -X github.com/monitorall/monitorall/internal/config.GitCommit=${GIT_COMMIT}" \
    -o /out/monitorall ./cmd/monitorall

# ————————————————— 阶段三：运行镜像 —————————————————
FROM debian:bookworm-20250224-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*

ENV TZ=Asia/Shanghai

WORKDIR /app
COPY --from=builder /out/monitorall /app/monitorall
COPY server/config.example.yaml /app/config.example.yaml

# data 目录挂载出来：SQLite + 密钥 + 证书
VOLUME ["/app/data"]

# HTTP / HTTPS
EXPOSE 8080 8443

ENTRYPOINT ["/app/monitorall"]
CMD ["--config", "/app/data/config.yaml"]
