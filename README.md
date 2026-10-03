# go-im - 高性能即时通讯系统

> 后续演进方向：基于 go-zero + gRPC + Eino，循序渐进构建简化版飞书。目标、功能范围、阶段路线和开发约定见 [项目方案](docs/project-plan.md)。下文介绍现有 IM 实现，规划中的功能尚未全部实现。

基于微服务架构思想设计的 Go 语言即时通讯（IM）系统后端。支持单聊、群聊，基于 WebSocket 实现消息实时推送，使用 Kafka 做消息削峰解耦，Redis 做在线状态管理和消息防重，MySQL 做持久化存储。

## 架构图

```mermaid
graph TB
    Client[客户端]

    subgraph Go-IM System
        API[API Server<br/>:8080]
        WS[WS Gateway<br/>:8081 / :9091]
        Push[Push Server]
    end

    subgraph Infrastructure
        MySQL[(MySQL)]
        Redis[(Redis)]
        Kafka[(Kafka)]
    end

    Client -->|HTTP REST| API
    Client -->|WebSocket| WS
    WS -->|写入消息| Kafka
    Kafka -->|消费消息| Push
    Push -->|持久化| MySQL
    Push -->|查在线状态| Redis
    Push -->|内部 HTTP 推送| WS
    Push -->|离线存储| MySQL
    WS -->|注册在线状态| Redis
    WS -->|消息去重| Redis
    API -->|用户/好友/群组 CRUD| MySQL
```

## 核心特性

- **旧链路三进程拆分**：API Server（HTTP 接口）、WS Gateway（长连接维持）、Push Server（异步消费推送），各自独立部署
- **发送请求去重**：客户端生成 `msg_id`，WS 网关借助 Redis 限制重复入队；Kafka/Push 重试仍可能重复在线推送，接收端按 `msg_id` 去重
- **离线消息拉取**：用户离线时消息写入 offline_messages 表，上线后通过 API 拉取
- **Kafka 削峰解耦**：WS 网关收到消息后写入 Kafka，Push 服务异步消费，避免网关阻塞
- **在线状态管理**：Redis 存储 `online:{user_id} → ws_addr`，心跳刷新 TTL，支持精准路由
- **群聊扇出推送**：Push 服务查询群成员列表（Redis 缓存优先），逐成员在线检查并推送/离线存储
- **JWT 认证**：API 和 WS 连接均使用 JWT Token 鉴权
- **Snowflake ID**：消息 ID 使用 Snowflake 算法生成，天然有序
- **结构化日志**：Zap + Lumberjack，关键业务节点带 Context 字段输出
- **Docker Compose 部署**：MySQL、Redis、Kafka、旧链路三个进程及阶段 1 的 API Gateway、用户 RPC 统一编排

## 技术栈

| 组件 | 技术选型 |
|------|---------|
| 语言 | Go 1.26.1 |
| HTTP 框架 | Gin |
| WebSocket | gorilla/websocket |
| ORM | GORM + MySQL 8.0 |
| 缓存 | Redis 7 (go-redis) |
| 消息队列 | Kafka (kafka-go) |
| 认证 | JWT v5 |
| 配置 | Viper |
| 日志 | Zap + Lumberjack |
| ID 生成 | Snowflake |
| 容器化 | Docker + Docker Compose |

## 项目结构

```text
go-im/
├── cmd/
│   ├── api/main.go              # HTTP API 服务入口
│   ├── ws/main.go               # WebSocket 网关入口
│   └── push/main.go             # 异步推送服务入口
├── api/                          # 迁移中的 go-zero API Gateway
├── rpc/user/                     # 用户 gRPC 服务
├── internal/
│   ├── config/                  # 配置加载
│   ├── model/                   # GORM 数据模型
│   ├── handler/                 # HTTP Handler（Gin）
│   ├── service/                 # 业务逻辑层
│   ├── repository/              # 数据访问层（MySQL + Redis）
│   ├── ws/                      # WebSocket 网关核心逻辑
│   ├── push/                    # 推送服务逻辑
│   ├── middleware/              # Gin 中间件
│   └── pkg/                     # 公共组件（JWT、响应、错误码、雪花ID、日志）
├── config/go-im.yaml            # 本地开发配置
├── deploy/
│   ├── docker-compose.yaml      # 容器编排
│   ├── Dockerfile               # 多阶段构建
│   ├── docker-config.yaml       # 旧服务的脱敏容器配置模板
│   ├── api-gateway.yaml         # 新 API Gateway 容器配置
│   ├── user-rpc.yaml            # 用户 RPC 容器配置
│   └── mysql/init.sql           # 数据库初始化
├── Makefile
└── README.md
```

## 快速开始

> 2026-09-20：`deploy` 已同步腾讯云部署基线。运行下列 Docker 命令前，先按 [部署配置说明](deploy/README.md) 准备私有配置和 `.env`。此 Compose 不再发布中间件端口，下面的主机直接运行方式需要另外配置本地端口映射。

### 方式一：Docker Compose 一键启动

```bash
# 全量启动（MySQL + Redis + Kafka + 五个 Go 进程）
make docker-up

# 查看日志
cd deploy && docker-compose logs -f

# 停止
make docker-down
```

### 方式二：本地开发

```bash
# 1. 启动中间件（MySQL + Redis + Kafka）
make env-up

# 2. 在不同终端分别启动三个服务
make run-api    # 终端 1：API 服务 → :8080
make run-ws     # 终端 2：WS 网关 → :8081 (WS) + :9091 (内部 RPC)
make run-push   # 终端 3：Push 服务

# 3. 停止中间件
make env-down
```

### 构建

```bash
# 编译三个服务到 bin/ 目录
make build

# 运行测试
make test
```

## API 接口

### 用户模块
| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/v1/user/register` | 用户注册 |
| POST | `/api/v1/user/login` | 用户登录 |
| GET | `/api/v1/user/info` | 获取用户信息 |

### 好友模块
| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/v1/friend/add` | 发送好友申请 |
| POST | `/api/v1/friend/accept` | 同意好友申请 |
| GET | `/api/v1/friend/list` | 获取好友列表 |

### 群组模块
| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/v1/group/create` | 创建群组 |
| POST | `/api/v1/group/join` | 加入群组 |
| GET | `/api/v1/group/list` | 我的群组列表 |
| GET | `/api/v1/group/members` | 获取群成员 |

### 消息模块
| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/v1/message/offline` | 拉取未确认的离线消息，不删除 |
| POST | `/api/v1/message/offline/ack` | 处理后用字符串消息 ID 确认，格式见[迁移说明](docs/im-migration.md) |
| GET | `/api/v1/message/history` | 历史消息（游标分页） |

### WebSocket
- 连接地址：`ws://host:8081/ws?token=<JWT_TOKEN>`
- 上行消息格式：`{"type": "chat", "data": {"msg_id": "...", "to_id": 123, "chat_type": 1, "content_type": 1, "content": "hello"}}`
- 下行消息格式：`{"type": "ack", "data": {"msg_id": "..."}}` / `{"type": "chat", "data": {...}}`

## 单聊消息时序图

```mermaid
sequenceDiagram
    participant A as 用户A (发送方)
    participant WS as WS Gateway
    participant K as Kafka
    participant PS as Push Server
    participant R as Redis
    participant DB as MySQL
    participant WS2 as WS Gateway
    participant B as 用户B (接收方)

    A->>WS: WebSocket 发送 ChatData
    WS->>R: 预约 msg_dedup:{msg_id} (发送中)
    R-->>WS: 预约成功
    WS->>K: 写入 chat_messages Topic
    WS->>R: 确认 msg_dedup:{msg_id} (已入队)
    WS-->>A: ACK {msg_id}

    K->>PS: 消费消息
    PS->>DB: INSERT INTO messages (持久化)
    PS->>R: GET online:{user_b_id}
    R-->>PS: ws_gateway_addr (在线)

    PS->>WS2: POST /internal/push (内部 HTTP)
    WS2->>B: WebSocket 推送 ServerMsg{type: "chat"}

    Note over PS,DB: 若用户B离线，则写入 offline_messages 表
```

## 统一响应格式

```json
{
    "code": 0,
    "msg": "success",
    "data": {}
}
```

- `code = 0` 表示成功
- `code != 0` 对应 `internal/pkg/errcode` 中定义的业务错误码

## License

MIT
