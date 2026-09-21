# MonitorAll — 多源异构监控看板平台（Go 后端 + 嵌入式 Vue3 前端）
# 多阶段构建：先由 Node 构建前端产物，再打进 Go 单二进制（go:embed）。

# ————————————————— 阶段一：构建前端产物 —————————————————
FROM node:22-alpine AS webbuilder
WORKDIR /src
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ————————————————— 阶段二：构建 Go 二进制（CGO_ENABLED=0，纯 Go SQLite） —————————————————
FROM golang:1.22-bookworm AS builder

ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOPROXY=https://goproxy.cn,direct

WORKDIR /src

# 先复制依赖清单，利用 Docker 层缓存
COPY server/go.mod server/go.sum ./
RUN go mod download

COPY server/ ./

# 嵌入前端产物
COPY --from=webbuilder /src/dist ./internal/web/dist

RUN go build -trimpath -ldflags "-s -w" -o /out/monitorall ./cmd/monitorall

# ————————————————— 阶段三：运行镜像 —————————————————
FROM debian:bookworm-slim

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
