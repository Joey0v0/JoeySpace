package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/gorilla/websocket"
	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/config"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/pkg/jwt"
	"github.com/yjydist/go-im/internal/repository"
	"github.com/yjydist/go-im/rpc/im/pb"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// upgrader WebSocket 升级配置
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许所有来源（生产环境应限制）
	},
}

// Server WebSocket 网关服务
type Server struct {
	hub         *Hub
	kafkaWriter *kafkaWriterAdapter
	redisRepo   repository.RedisRepository
	wsRPCAddr   string
	jwtSecret   string
	imConn      *grpc.ClientConn
	imClient    pb.IMClient
	logger      *zap.Logger
}

// kafkaWriterAdapter 适配 kafka-go Writer 到 KafkaWriter 接口
type kafkaWriterAdapter struct {
	writer *kafka.Writer
}

func (a *kafkaWriterAdapter) WriteMessages(ctx context.Context, msgs ...KafkaMessage) error {
	kafkaMsgs := make([]kafka.Message, len(msgs))
	for i, m := range msgs {
		kafkaMsgs[i] = kafka.Message{
			Key:   m.Key,
			Value: m.Value,
		}
	}
	return a.writer.WriteMessages(ctx, kafkaMsgs...)
}

// NewServer 创建 WS 网关服务
func NewServer(cfg *config.Config, hub *Hub, redisRepo repository.RedisRepository, logger *zap.Logger) *Server {
	// 创建 Kafka Writer
	writer := kafka.NewWriter(kafka.WriterConfig{
		Brokers:  cfg.Kafka.Brokers,
		Topic:    cfg.Kafka.TopicChat,
		Balancer: &kafka.LeastBytes{},
	})

	wsRPCAddr := os.Getenv("WS_RPC_ADDR")
	if wsRPCAddr == "" {
		wsRPCAddr = fmt.Sprintf("localhost:%d", cfg.WSServer.RPCPort)
	}
	var imConn *grpc.ClientConn
	var imClient pb.IMClient
	if addr := os.Getenv("IM_RPC_ADDR"); addr != "" {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			logger.Error("create IM RPC client failed", zap.Error(err))
		} else {
			imConn = conn
			imClient = pb.NewIMClient(conn)
		}
	}

	return &Server{
		hub:         hub,
		kafkaWriter: &kafkaWriterAdapter{writer: writer},
		redisRepo:   redisRepo,
		wsRPCAddr:   wsRPCAddr,
		jwtSecret:   cfg.JWT.Secret,
		imConn:      imConn,
		imClient:    imClient,
		logger:      logger,
	}
}

// HandleWS 处理 WebSocket 升级请求
// 客户端通过 URL query 传递 JWT token 进行鉴权
// ws://host:port/ws?token=xxx
func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	// 从 URL query 获取 token
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}

	// 验证 JWT
	claims, err := jwt.ParseToken(token, s.jwtSecret)
	if err != nil {
		s.logger.Warn("ws auth failed",
			zap.String("remote_addr", r.RemoteAddr),
			zap.Error(err),
		)
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	// 升级为 WebSocket 连接
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("ws upgrade failed",
			zap.Int64("user_id", claims.UserID),
			zap.Error(err),
		)
		return
	}

	s.logger.Info("ws connection established",
		zap.Int64("user_id", claims.UserID),
		zap.String("remote_addr", r.RemoteAddr),
	)

	// 创建 Client 并启动
	client := NewClient(claims.UserID, token, conn, s.hub, s.kafkaWriter, s.redisRepo, s.imClient, s.wsRPCAddr, s.logger)
	s.hub.Register(client)
	client.Start()
}

// HandleInternalPush 处理内部推送请求
// Push 服务通过此接口将消息推送给在线用户
// POST /internal/push
// Body: {"user_id": 123, "data": {...}}
func (s *Server) HandleInternalPush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var msg PushMsg
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// 通过 Hub 查找对应用户的连接
	client, ok := s.hub.GetClient(msg.UserID)
	if !ok {
		// 用户不在本节点
		http.Error(w, "user not connected", http.StatusNotFound)
		return
	}

	// 将内部消息中的整数 ID 精确转换为浏览器可安全解析的字符串。
	var chat model.Message
	if err := json.Unmarshal(msg.Data, &chat); err != nil {
		http.Error(w, "invalid chat data", http.StatusBadRequest)
		return
	}
	// 序列化下行消息
	serverMsg := ServerMsg{
		Type: "chat",
		Data: browserChatMessage(chat),
	}
	data, err := json.Marshal(serverMsg)
	if err != nil {
		s.logger.Error("marshal push msg failed",
			zap.Int64("user_id", msg.UserID),
			zap.Error(err),
		)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// 仅在成功进入写队列时确认，失败则让 Push 保存离线消息。
	if !client.Send(data) {
		http.Error(w, "client send queue unavailable", http.StatusServiceUnavailable)
		return
	}

	s.logger.Debug("pushed message to client",
		zap.Int64("user_id", msg.UserID),
	)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"code":0,"msg":"ok"}`))
}

// Close 关闭 Kafka Writer
func (s *Server) Close() error {
	if s.imConn != nil {
		s.imConn.Close()
	}
	return s.kafkaWriter.writer.Close()
}
