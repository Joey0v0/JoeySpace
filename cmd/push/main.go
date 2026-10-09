package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
	if err := runPush(); err != nil {
		log.Fatal(err)
	}
}

func runPush() error {
	configPath := flag.String("config", "config/go-im.yaml", "config file path")
	flag.Parse()

	// 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config failed: %w", err)
	}
	// Fail before connecting infrastructure if the independent trigger topic is unsafe.
	if err := validateAgentTriggerConfig(cfg.Kafka); err != nil {
		return fmt.Errorf("invalid agent trigger config: %w", err)
	}
	if err := config.ValidateTaskNotificationPush(cfg); err != nil {
		return err
	}
	eligibilityConfig, err := push.LoadTeamEligibilityClientConfig(os.Getenv)
	if err != nil {
		return err
	}
	eligibility, err := push.NewTeamEligibilityClient(eligibilityConfig)
	if err != nil {
		return err
	}
	defer eligibility.Close()
	mentionConfig, err := push.LoadMentionClientConfig(os.Getenv)
	if err != nil {
		return err
	}
	mentionClient, err := push.NewMentionClient(mentionConfig)
	if err != nil {
		return err
	}
	defer mentionClient.Close()

	// 初始化日志
	if err := logger.Init(cfg.Log.Level, cfg.Log.Filename); err != nil {
		return fmt.Errorf("init logger failed: %w", err)
	}
	defer logger.Sync()

	// Certificates and role configuration fail before infrastructure startup.
	notifications, err := prepareTaskNotificationPush(cfg, logger.L, taskNotificationPushFactories{})
	if err != nil {
		return err
	}
	if notifications != nil {
		defer notifications.Close()
	}

	// 初始化 Snowflake（Push 服务持久化消息时需要生成 ID）
	if err := snowflake.Init(cfg.App.ServerID); err != nil {
		return fmt.Errorf("init snowflake failed: %w", err)
	}

	// 初始化 MySQL
	if err := repository.InitMySQL(&cfg.MySQL, logger.L); err != nil {
		return fmt.Errorf("init mysql failed: %w", err)
	}

	// 初始化 Redis
	if err := repository.InitRedis(&cfg.Redis, logger.L); err != nil {
		return fmt.Errorf("init redis failed: %w", err)
	}

	// 创建 Repository
	messaging, err := newPushMessaging(cfg.Kafka, repository.DB, logger.L, agentTriggerFactories{})
	if err != nil {
		return fmt.Errorf("init push messaging failed: %w", err)
	}
	groupRepo := repository.NewGroupRepository()
	redisRepo := repository.NewRedisRepository()

	// 创建 Pusher
	pusher := push.NewPusher(messaging.messages, groupRepo, redisRepo, logger.L)
	pusher.SetTeamEligibility(eligibility)
	pusher.SetMentionValidator(mentionClient)

	// 创建 Kafka Consumer
	consumer := push.NewConsumer(
		cfg.Kafka.Brokers,
		cfg.Kafka.TopicChat,
		cfg.Kafka.ConsumerGroup,
		pusher,
		logger.L,
	)

	// 使用 context 控制优雅退出
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if notifications != nil {
		if err := notifications.Start(ctx, redisRepo); err != nil {
			_ = consumer.Close()
			if messaging.writer != nil {
				_ = messaging.writer.Close()
			}
			return err
		}
	}

	// 启动消息消费及可选的 Outbox 发布循环
	waitAndClose := startPushWorkers(ctx, consumer, messaging.publisher, messaging.writer)

	logger.L.Sugar().Info("Push service started")

	// 等待退出信号
	<-ctx.Done()

	logger.L.Sugar().Info("Push service shutting down...")
	cancel()
	var notificationErr error
	if notifications != nil {
		notificationErr = notifications.Close()
	}
	return errors.Join(notificationErr, waitAndClose())
}
