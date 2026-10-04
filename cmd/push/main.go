package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/yjydist/go-im/internal/config"
	"github.com/yjydist/go-im/internal/pkg/logger"
	"github.com/yjydist/go-im/internal/pkg/snowflake"
	"github.com/yjydist/go-im/internal/push"
	"github.com/yjydist/go-im/internal/repository"
)

func main() {
	configPath := flag.String("config", "config/go-im.yaml", "config file path")
	flag.Parse()

	// 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config failed: %v", err)
	}
	// Fail before connecting infrastructure if the independent trigger topic is unsafe.
	if err := validateAgentTriggerConfig(cfg.Kafka); err != nil {
		log.Fatalf("invalid agent trigger config: %v", err)
	}

	// 初始化日志
	if err := logger.Init(cfg.Log.Level, cfg.Log.Filename); err != nil {
		log.Fatalf("init logger failed: %v", err)
	}
	defer logger.Sync()

	// 初始化 Snowflake（Push 服务持久化消息时需要生成 ID）
	if err := snowflake.Init(cfg.App.ServerID); err != nil {
		log.Fatalf("init snowflake failed: %v", err)
	}

	// 初始化 MySQL
	if err := repository.InitMySQL(&cfg.MySQL, logger.L); err != nil {
		log.Fatalf("init mysql failed: %v", err)
	}

	// 初始化 Redis
	if err := repository.InitRedis(&cfg.Redis, logger.L); err != nil {
		log.Fatalf("init redis failed: %v", err)
	}

	// 创建 Repository
	messaging, err := newPushMessaging(cfg.Kafka, repository.DB, logger.L, agentTriggerFactories{})
	if err != nil {
		log.Fatalf("init push messaging failed: %v", err)
	}
	groupRepo := repository.NewGroupRepository()
	redisRepo := repository.NewRedisRepository()

	// 创建 Pusher
	pusher := push.NewPusher(messaging.messages, groupRepo, redisRepo, logger.L)

	// 创建 Kafka Consumer
	consumer := push.NewConsumer(
		cfg.Kafka.Brokers,
		cfg.Kafka.TopicChat,
		cfg.Kafka.ConsumerGroup,
		pusher,
		logger.L,
	)

	// 使用 context 控制优雅退出
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 启动消息消费及可选的 Outbox 发布循环
	waitAndClose := startPushWorkers(ctx, consumer, messaging.publisher, messaging.writer)

	logger.L.Sugar().Info("Push service started")

	// 等待退出信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)
	<-quit

	logger.L.Sugar().Info("Push service shutting down...")
	cancel()
	if err := waitAndClose(); err != nil {
		logger.L.Sugar().Errorf("close push resources failed: %v", err)
	}
}
