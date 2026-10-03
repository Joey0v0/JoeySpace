package repository

import (
	"fmt"

	"github.com/redis/go-redis/v9"
	"github.com/yjydist/go-im/internal/config"
	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	DB  *gorm.DB
	RDB *redis.Client
)

// InitMySQL 初始化 MySQL 连接
func InitMySQL(cfg *config.MySQLConfig, zapLogger *zap.Logger) error {
	var gormLogLevel logger.LogLevel
	if config.GlobalConfig.App.Env == "debug" {
		gormLogLevel = logger.Info
	} else {
		gormLogLevel = logger.Warn
	}

	db, err := gorm.Open(mysql.Open(cfg.DSN), &gorm.Config{
		Logger: logger.Default.LogMode(gormLogLevel),
	})
	if err != nil {
		return fmt.Errorf("connect mysql failed: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get sql.DB failed: %w", err)
	}

	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)

	DB = db
	zapLogger.Info("mysql connected")
	return nil
}

// InitRedis 初始化 Redis 连接
func InitRedis(cfg *config.RedisConfig, zapLogger *zap.Logger) error {
	RDB = redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	zapLogger.Info("redis connected")
	return nil
}
