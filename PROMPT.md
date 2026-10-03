# 项目：go-im - 高性能即时通讯系统

## 一、项目概述

使用 Go 实现一个即时通讯系统后端，支持单聊、群聊，基于 WebSocket 实现消息实时推送，使用 Kafka 做消息削峰解耦，Redis 做在线状态管理和消息防重，MySQL 做持久化存储。
系统拆分为三个微服务组件：API 服务（处理 HTTP 请求）、WS 网关（维持长连接）、Push 服务（异步消费消息并推送）。

## 二、技术栈

- 语言：Go 1.22+
- HTTP 框架：Gin (`github.com/gin-gonic/gin`)
- WebSocket 库：`github.com/gorilla/websocket`
- ORM：GORM (`gorm.io/gorm`) + MySQL 驱动
- 数据库：MySQL 8.0
- 缓存：Redis 7 (`github.com/redis/go-redis/v9`)
- 消息队列：Kafka (`github.com/segmentio/kafka-go`)
- 认证：JWT (`github.com/golang-jwt/jwt/v5`)
- 配置：Viper (`github.com/spf13/viper`)
- 日志：Zap (`go.uber.org/zap`) + Lumberjack 日志切割
- 参数校验：validator (Gin 内置)
- API 文档：Swagger (`github.com/swaggo/swag`)
- ID 生成：Snowflake (`github.com/bwmarrin/snowflake`)
- 容器化：Docker + Docker Compose

## 三、项目结构

```text  
go-im/  
├── cmd/  
│   ├── api/  
│   │   └── main.go              // HTTP API 服务启动入口  
│   ├── ws/  
│   │   └── main.go              // WebSocket 网关启动入口  
│   └── push/  
│       └── main.go              // 异步推送服务启动入口  
├── internal/  
│   ├── config/  
│   │   └── config.go            // 统一配置结构体与加载逻辑  
│   ├── model/                   // GORM 数据模型  
│   ├── handler/                 // HTTP 处理器 (Gin)  
│   ├── service/                 // 核心业务逻辑层 (Interface + Impl)  
│   ├── repository/              // 数据访问层 (MySQL + Redis 交互)  
│   ├── ws/                      // WebSocket 网关核心逻辑  
│   │   ├── server.go            // WS 监听与服务  
│   │   ├── hub.go               // 连接管理器  
│   │   ├── client.go            // 单个长连接对象  
│   │   └── protocol.go          // WS 消息协议定义  
│   ├── push/                    // 推送服务逻辑  
│   │   ├── consumer.go          // Kafka 消费者  
│   │   └── pusher.go            // 路由与推送逻辑  
│   ├── middleware/              // Gin 中间件 (Auth, CORS, Logger)  
│   └── pkg/                     // 公共组件  
│       ├── jwt/  
│       ├── response/  
│       ├── errcode/  
│       └── snowflake/  
├── config/  
│   └── go-im.yaml               // 本地开发配置文件  
├── deploy/  
│   ├── docker-compose.yaml      // 中间件及应用编排  
│   ├── mysql/  
│   │   └── init.sql             // 数据库初始化脚本  
│   └── Dockerfile               // 多阶段构建 Dockerfile  
├── docs/                        // Swagger 文档目录 (自动生成)  
├── Makefile  
├── go.mod  
├── go.sum  
└── README.md  
```

## 四、配置文件 (config/go-im.yaml)

提供一份统一的配置文件，供三个服务读取：

```yaml  
app:  
  env: "debug"               # debug | release  
  server_id: 1               # 用于 Snowflake ID 生成  

api_server:  
  port: 8080  

ws_server:  
  port: 8081  
  rpc_port: 9091             # 用于接收内部 push 请求的 HTTP/RPC 端口  

mysql:  
  dsn: "root:root123@tcp(localhost:3306)/go_im?charset=utf8mb4&parseTime=True&loc=Local"  
  max_open_conns: 100  
  max_idle_conns: 20  

redis:  
  addr: "localhost:6379"  
  password: ""  
  db: 0  

kafka:  
  brokers: ["localhost:9092"]  
  topic_chat: "chat_messages"  
  consumer_group: "im_push_group"  

jwt:  
  secret: "im_super_secret_key"  
  access_expire_hours: 24  
  refresh_expire_hours: 168  

log:  
  level: "debug"  
  filename: "./logs/go-im.log"  
```

## 五、数据库设计 (deploy/mysql/init.sql)

```sql  
CREATE DATABASE IF NOT EXISTS go_im DEFAULT CHARSET utf8mb4 COLLATE utf8mb4_unicode_ci;  
USE go_im;  

-- 用户表  
CREATE TABLE users (  
    id         BIGINT PRIMARY KEY,                 -- Snowflake ID  
    username   VARCHAR(32)  NOT NULL UNIQUE,  
    password   VARCHAR(128) NOT NULL,              -- bcrypt hash  
    nickname   VARCHAR(64)  NOT NULL DEFAULT '',  
    avatar     VARCHAR(256) NOT NULL DEFAULT '',  
    status     TINYINT      NOT NULL DEFAULT 1,    -- 1:正常 2:封禁  
    created_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,  
    updated_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,  
    INDEX idx_username (username)  
) ENGINE=InnoDB;  

-- 好友关系表  
CREATE TABLE friendships (  
    id         BIGINT PRIMARY KEY AUTO_INCREMENT,  
    user_id    BIGINT   NOT NULL,  
    friend_id  BIGINT   NOT NULL,  
    status     TINYINT  NOT NULL DEFAULT 0,        -- 0:待确认 1:已接受  
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,  
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,  
    UNIQUE KEY uk_user_friend (user_id, friend_id),  
    INDEX idx_friend_id (friend_id)  
) ENGINE=InnoDB;  

-- 群组表  
CREATE TABLE `groups` (  
    id          BIGINT PRIMARY KEY,                -- Snowflake ID  
    name        VARCHAR(64) NOT NULL,  
    owner_id    BIGINT      NOT NULL,  
    avatar      VARCHAR(256) NOT NULL DEFAULT '',  
    created_at  TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP  
) ENGINE=InnoDB;  

-- 群成员表  
CREATE TABLE group_members (  
    id         BIGINT PRIMARY KEY AUTO_INCREMENT,  
    group_id   BIGINT  NOT NULL,  
    user_id    BIGINT  NOT NULL,  
    role       TINYINT NOT NULL DEFAULT 0,         -- 0:普通 1:管理员 2:群主  
    joined_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,  
    UNIQUE KEY uk_group_user (group_id, user_id)  
) ENGINE=InnoDB;  

-- 消息主表（真实项目会分表，这里单表即可）  
CREATE TABLE messages (  
    id           BIGINT PRIMARY KEY,                -- Snowflake ID，自然有序  
    msg_id       VARCHAR(64)  NOT NULL UNIQUE,      -- 客户端生成的 UUID，防重幂等  
    from_id      BIGINT       NOT NULL,  
    to_id        BIGINT       NOT NULL,             -- 接收人ID 或 群组ID  
    chat_type    TINYINT      NOT NULL,             -- 1:单聊 2:群聊  
    content_type TINYINT      NOT NULL DEFAULT 1,   -- 1:文本 2:图片 3:文件  
    content      TEXT         NOT NULL,  
    created_at   TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,  
    INDEX idx_to_time (to_id, created_at)  
) ENGINE=InnoDB;  

-- 离线消息表  
CREATE TABLE offline_messages (  
    id         BIGINT PRIMARY KEY AUTO_INCREMENT,  
    user_id    BIGINT  NOT NULL,                    -- 接收方ID  
    message_id BIGINT  NOT NULL,                    -- 关联 messages 表的 ID  
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,  
    INDEX idx_user_id (user_id)  
) ENGINE=InnoDB;  
```

## 六、各模块详细设计

### 6.1 Redis 键空间设计

| Key 模式 | 数据结构 | 作用 | TTL |
|----------|------|------|-----|
| `online:{user_id}` | STRING | 在线状态映射，Value 存 `WS网关节点内部IP:Port` | 120s（客户端心跳刷新） |
| `msg_dedup:{msg_id}` | STRING | 消息处理防重去重，Value 为 `1` | 5分钟 |
| `group:members:{group_id}` | SET | 缓存群成员 ID 列表，加速群发 | 1小时 |

### 6.2 WebSocket 网关 (WS Server)

负责维持海量长连接、初步校验与转发。

**协议定义 (internal/ws/protocol.go)：**
```go  
// 客户端上行消息  
type ClientMsg struct {  
    Type string          `json:"type"`  // "chat" (聊天), "ack" (已读确认), "ping" (心跳)  
    Data json.RawMessage `json:"data"`  
}  

type ChatData struct {  
    MsgID       string `json:"msg_id"`  
    ToID        int64  `json:"to_id"`  
    ChatType    int    `json:"chat_type"`    // 1:单聊 2:群聊  
    ContentType int    `json:"content_type"`  
    Content     string `json:"content"`  
}  

// 服务端下行消息  
type ServerMsg struct {  
    Type string      `json:"type"`  // "chat" (新消息), "ack" (服务端确认接收), "error" (错误信息)  
    Data interface{} `json:"data"`  
}  
```

**连接管理 (Hub & Client)：**
- 使用 `gorilla/websocket`。
- 连接建立：鉴权（URL query 传 JWT token），成功后将连接注册到 Hub。
- Redis 注册：连接成功后，执行 `SET online:{user_id} {ws_rpc_port} EX 120`。
- Client 使用两个 Goroutine：`readPump` 循环读取客户端消息，`writePump` 从发送通道读取消息发给客户端。

**消息接收逻辑 (readPump)：**
1. 收到 `ClientMsg`，解析为 `ChatData`。
2. 幂等校验：`SETNX msg_dedup:{msg_id} 1`，如果已存在，说明是重复消息，直接给客户端回 `ack` 即可，不往下走。
3. 封装完整消息对象（补充发送者 ID、服务端时间）。
4. 序列化为 JSON，写入 Kafka Topic `chat_messages`。
5. 往客户端下发 `ServerMsg{Type: "ack", Data: {msg_id: "..."}}` 表示服务器已接收。

**内部 Push 接收接口：**
WS 网关需要启动一个内部 HTTP/RPC 接口（如 9091 端口），提供 `POST /internal/push` 接口。
当 Push 服务发现某用户连接在本网关时，会调用此接口，网关收到后通过 Hub 找到对应 Client，将消息放入 `writePump` 通道推送给该用户。

### 6.3 异步推送服务 (Push Server)

负责消费 Kafka 消息，持久化入库，并精准推送。

**消费逻辑 (Consumer & Pusher)：**
1. 启动 Kafka Consumer 监听 `chat_messages`。
2. 收到消息，反序列化。
3. **入库持久化**：使用 GORM 将消息写入 MySQL `messages` 表。
4. **路由推送**：
   - **单聊** (ChatType=1)：
     1. 查询 Redis `GET online:{to_id}`。
     2. 若在线：获取到了 WS 网关地址，向该网关发起内部 HTTP 请求转发消息。
     3. 若离线：将记录写入 MySQL `offline_messages` 表。
   - **群聊** (ChatType=2)：
     1. 从 Redis `SMEMBERS group:members:{to_id}` 获取所有成员 ID（若缓存没命中，查 MySQL 并回填 Redis）。
     2. 遍历成员 ID，除了发送者自己，对每个人执行类似单聊的在线检查和推送/离线存储逻辑。
     *(进阶：群发时可以按目标 WS 网关地址聚合，批量发送以减少内部网络 RPC 调用)*。

### 6.4 API 服务 (API Server)

使用 Gin 框架，提供 RESTful 接口供客户端调用。所有非公开接口需使用 JWT 中间件保护。

**统一响应格式 (internal/pkg/response/response.go)：**
```go  
type Response struct {  
    Code int         `json:"code"`  
    Msg  string      `json:"msg"`  
    Data interface{} `json:"data,omitempty"`  
}  
// code = 0 表示成功，非 0 对应 internal/pkg/errcode 错误码  
```

**核心接口列表：**

*用户模块 (User)*
- `POST /api/v1/user/register`：注册（密码 bcrypt 加密）
- `POST /api/v1/user/login`：登录，返回 JWT access_token
- `GET  /api/v1/user/info`：获取本人或他人基本信息

*好友模块 (Friend)*
- `POST /api/v1/friend/add`：发送好友申请
- `POST /api/v1/friend/accept`：同意好友申请
- `GET  /api/v1/friend/list`：获取好友列表

*群组模块 (Group)*
- `POST /api/v1/group/create`：创建群组（记录群表并在 member 表插入群主）
- `POST /api/v1/group/join`：加入/拉人进群
- `GET  /api/v1/group/list`：获取我加入的群组列表
- `GET  /api/v1/group/members?group_id=xxx`：获取群成员列表

*消息模块 (Message)*
- `GET  /api/v1/message/offline`：拉取离线消息（拉取后，需在服务端清除离线表相关记录）
- `GET  /api/v1/message/history?target_id=xxx&chat_type=1&cursor_msg_id=xxx&limit=20`：游标分页拉取历史消息。

### 6.5 业务逻辑层拆分规范

遵守三层架构：
- **Handler 层 (`internal/handler`)**：只做入参 Bind/校验 (Gin validator)，调用 Service，封装统一 Response 格式返回。
- **Service 层 (`internal/service`)**：核心业务逻辑。如建群时，既要写 group 表又要写 group_member 表，需开启数据库事务。
- **Repository 层 (`internal/repository`)**：封装对 MySQL 和 Redis 的具体操作。Service 层不直接写 SQL。

## 七、部署与容器化 (Docker Compose)

在 `deploy/docker-compose.yaml` 中定义完整环境：

```yaml  
version: '3.8'  
services:  
  # 基础中间件  
  mysql:  
    image: mysql:8.0  
    ports: ["3306:3306"]  
    environment:  
      MYSQL_ROOT_PASSWORD: root123  
    volumes:  
      - ./mysql/init.sql:/docker-entrypoint-initdb.d/init.sql  
  
  redis:  
    image: redis:7-alpine  
    ports: ["6379:6379"]  

  kafka:  
    image: bitnami/kafka:latest  
    ports: ["9092:9092"]  
    environment:  
      - KAFKA_ENABLE_KRAFT=yes  
      - KAFKA_CFG_PROCESS_ROLES=broker,controller  
      - KAFKA_CFG_CONTROLLER_LISTENER_NAMES=CONTROLLER  
      - KAFKA_CFG_LISTENERS=PLAINTEXT://:9092,CONTROLLER://:9093  
      - KAFKA_CFG_LISTENER_SECURITY_PROTOCOL_MAP=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT  
      - KAFKA_CFG_ADVERTISED_LISTENERS=PLAINTEXT://kafka:9092  
      - KAFKA_BROKER_ID=1  
      - KAFKA_CFG_CONTROLLER_QUORUM_VOTERS=1@kafka:9093  
      - ALLOW_PLAINTEXT_LISTENER=yes  

  # 三个 Go 服务 (需编写对应的多阶段 Dockerfile)  
  im-api:  
    build:   
      context: ..  
      dockerfile: deploy/Dockerfile  
      target: api  
    ports: ["8080:8080"]  
    depends_on: [mysql, redis]  

  im-ws:  
    build:   
      context: ..  
      dockerfile: deploy/Dockerfile  
      target: ws  
    ports: ["8081:8081", "9091:9091"]  
    depends_on: [redis, kafka]  

  im-push:  
    build:   
      context: ..  
      dockerfile: deploy/Dockerfile  
      target: push  
    depends_on: [mysql, redis, kafka]  
```

## 八、Makefile 与快捷指令

```makefile
.PHONY: build run-api run-ws run-push docs test env-up env-down  

# 启动中间件环境  
env-up:  
	cd deploy && docker-compose up -d mysql redis kafka  

env-down:  
	cd deploy && docker-compose down  

# 生成 Swagger 文档 (需先安装 swag)  
docs:  
	swag init -g cmd/api/main.go -o docs/  

# 本地启动三个服务（假设你在不同的终端运行）  
run-api:  
	go run cmd/api/main.go  

run-ws:  
	go run cmd/ws/main.go  

run-push:  
	go run cmd/push/main.go  

test:  
	go test ./... -v -cover  
```

## 九、测试与接口文档要求

1. **接口文档**：使用 `swaggo/swag` 在 `handler` 的函数上打注释，API 启动时挂载 `/swagger/*any` 路由。
2. **中间件测试**：对 JWT 鉴权中间件编写单元测试，验证 Token 过期、伪造 Token 的拒绝情况。
3. **WS 路由测试**：可以提供一个 `test_client.go` 脚本或使用前端 HTML/JS 写一个极简聊天页面放入 `examples/` 目录，展示建立连接、发送聊天和收到 ACK 的全过程。

## 十、README.md 撰写要求

1. **项目介绍**：说明是基于微服务架构思想设计的 Go 语言 IM 系统。
2. **架构图**：使用 Mermaid 语法画出 Client -> API/WS -> Kafka -> Push -> DB/Redis 的数据流向图。
3. **核心特性**：消息防重（幂等）、离线消息拉取、多组件拆分部署。
4. **快速开始**：说明如何使用 Docker Compose 一键拉起环境并测试。
5. **时序图**：使用 Mermaid Sequence Diagram 画出发送单聊消息的完整生命周期（从用户A发出到用户B收到的过程）。

## 十一、代码规范

- **错误处理**：禁止在业务层直接 `panic`，必须 `return err`，并在 API 顶层捕获打印日志，返回统一的 JSON 错误码。
- **配置注入**：不要在代码中写死硬编码，超时时间、端口、秘钥均需从 Viper 配置读取。
- **并发控制**：WebSocket `Client` 中的 goroutine 必须监听 `closeCh` 等待关闭信号，防止 Goroutine 泄漏。
- **事务处理**：GORM 涉及多表更新的操作必须包裹在 `db.Transaction(func(tx *gorm.DB) error { ... })` 中。
- **日志标准**：关键业务节点（如收到建立连接请求、投递 Kafka 失败、发送 Push 失败）必须使用 Zap 输出带有 Context（如 user_id, msg_id）的字段化日志，不要只用 `fmt.Println`。