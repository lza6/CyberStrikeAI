# syntax=docker/dockerfile:1
# CyberStrikeAI 容器镜像（多阶段构建）
#
# 用法：
#   docker build -t cyberstrike-ai .
#   docker run --rm -p 8080:8080 -v "$PWD/data:/app/data" cyberstrike-ai
#
# 说明：
# - 构建阶段使用 golang:1.25-bookworm，需 CGO（mattn/go-sqlite3）因此不能用 alpine 默认 musl。
# - 运行阶段用 debian:bookworm-slim，仅装 ca-certificates（HTTPS 出站所需）。
# - 数据目录 /app/data 建议挂载为 volume，避免容器重建丢失会话/知识库。
# - 默认入口走 config.yaml + --http；如需 TLS 请在 config.yaml 配置 server.tls_*。

# ---------- 构建阶段 ----------
FROM golang:1.25-bookworm AS builder

# CGO 依赖（mattn/go-sqlite3 需要 gcc）
RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# 先拷 go.mod/go.sum 利用层缓存加速依赖下载
COPY go.mod go.sum ./
RUN go mod download

# 拷源码（.dockerignore 已排除 data/ dist/ .git 等）
COPY . .

# 静态资源与配置目录在运行阶段单独 COPY；此处只编二进制
ENV CGO_ENABLED=1 GOOS=linux GOARCH=amd64
RUN go build -trimpath -ldflags="-s -w" -o /out/cyberstrike-ai ./cmd/server

# ---------- 运行阶段 ----------
FROM debian:bookworm-slim

# ca-certificates：HTTPS 出站（OpenAI/飞书/钉钉等回调）
# tzdata：日志时间按本地时区可读（容器默认 UTC）
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# 二进制
COPY --from=builder /out/cyberstrike-ai /app/cyberstrike-ai

# 运行时只读资源（来自仓库，非 data/ 运行时数据）
COPY --from=builder /src/web ./web
COPY --from=builder /src/tools ./tools
COPY --from=builder /src/skills ./skills
COPY --from=builder /src/agents ./agents
COPY --from=builder /src/roles ./roles
COPY --from=builder /src/playbooks ./playbooks
COPY --from=builder /src/config.example.yaml ./config.yaml

# 数据目录（建议挂载 volume）
RUN mkdir -p /app/data
VOLUME ["/app/data"]

EXPOSE 8080

ENTRYPOINT ["/app/cyberstrike-ai", "-config", "config.yaml", "--http"]
