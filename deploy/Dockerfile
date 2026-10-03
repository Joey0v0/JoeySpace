# ============================================
# 第一阶段：统一编译
# ============================================
FROM golang:1.26.1-alpine AS builder
ENV GOMAXPROCS=2
ENV GOFLAGS="-p=1"

RUN apk add --no-cache git

WORKDIR /app

# 先复制依赖文件，利用 Docker 层缓存
COPY go.mod go.sum ./
ENV GOPROXY=https://goproxy.cn,direct
RUN go mod download

# 复制源码
COPY . .

# 编译旧链路的三个服务
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/im-api   ./cmd/api/
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/im-ws    ./cmd/ws/
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/im-push  ./cmd/push/

# 编译阶段 1 新增的 API Gateway 与用户 RPC
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/api-gateway ./api/
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/user-rpc   ./rpc/user/
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/im-rpc     ./rpc/im/
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/task-rpc   ./rpc/task/
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/agent-rpc  ./cmd/agent/

# ============================================
# 第二阶段：API 服务
# ============================================
FROM alpine:3.19 AS api

RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

COPY --from=builder /bin/im-api .
COPY config/go-im.yaml config/go-im.yaml

EXPOSE 8080
CMD ["./im-api", "-config", "config/go-im.yaml"]

# ============================================
# 第三阶段：WS 网关
# ============================================
FROM alpine:3.19 AS ws

RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

COPY --from=builder /bin/im-ws .
COPY config/go-im.yaml config/go-im.yaml

EXPOSE 8081 9091
CMD ["./im-ws", "-config", "config/go-im.yaml"]

# ============================================
# 第四阶段：Push 服务
# ============================================
FROM alpine:3.19 AS push

RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

COPY --from=builder /bin/im-push .
COPY config/go-im.yaml config/go-im.yaml

CMD ["./im-push", "-config", "config/go-im.yaml"]

# ============================================
# 第五阶段：迁移中的 go-zero API Gateway
# ============================================
FROM alpine:3.19 AS api-gateway

RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

COPY --from=builder /bin/api-gateway .
COPY deploy/api-gateway.yaml etc/api.yaml

EXPOSE 8082
CMD ["./api-gateway", "-f", "etc/api.yaml"]

# ============================================
# 第六阶段：用户 RPC
# ============================================
FROM alpine:3.19 AS user-rpc

RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

COPY --from=builder /bin/user-rpc .
COPY deploy/user-rpc.yaml etc/user.yaml

EXPOSE 9001
CMD ["./user-rpc", "-f", "etc/user.yaml", "-profile"]

# ============================================
# 第七阶段：IM RPC（旧群成员权限）
# ============================================
FROM alpine:3.19 AS im-rpc

RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

COPY --from=builder /bin/im-rpc .
COPY deploy/im-rpc.yaml etc/im.yaml

EXPOSE 9002
CMD ["./im-rpc", "-f", "etc/im.yaml"]

# ============================================
# 第八阶段：任务 RPC
# ============================================
FROM alpine:3.19 AS task-rpc

RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

COPY --from=builder /bin/task-rpc .
COPY deploy/task-rpc.yaml etc/task.yaml

EXPOSE 9003
CMD ["./task-rpc", "-f", "etc/task.yaml"]

# ============================================
# 第九阶段：Agent RPC
# ============================================
FROM alpine:3.19 AS agent-rpc

RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app

COPY --from=builder /bin/agent-rpc .
COPY deploy/agent-rpc.yaml etc/agent.yaml

EXPOSE 9004
CMD ["./agent-rpc", "-f", "etc/agent.yaml"]
