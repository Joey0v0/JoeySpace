package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/yjydist/go-im/internal/config"
	"github.com/yjydist/go-im/internal/pkg/logger"
	"github.com/yjydist/go-im/internal/repository"
	"github.com/yjydist/go-im/internal/ws"
)

func main() {
	configPath := flag.String("config", "config/go-im.yaml", "config file path")
	flag.Parse()

	// 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config failed: %v", err)
	}

	// 初始化日志
	if err := logger.Init(cfg.Log.Level, cfg.Log.Filename); err != nil {
		log.Fatalf("init logger failed: %v", err)
	}
	defer logger.Sync()

	// 初始化 Redis（WS 网关需要 Redis 来管理在线状态和消息去重）
	if err := repository.InitRedis(&cfg.Redis, logger.L); err != nil {
		log.Fatalf("init redis failed: %v", err)
	}

	// 创建 Hub 并启动
	hub := ws.NewHub(logger.L)
	go hub.Run()

	// 创建 RedisRepo
	redisRepo := repository.NewRedisRepository()

	// 创建 WS Server
	server := ws.NewServer(cfg, hub, redisRepo, logger.L)
	defer server.Close()

	// 注册 WebSocket 路由
	wsMux := http.NewServeMux()
	wsMux.HandleFunc("/ws", server.HandleWS)

	// 启动 WS 对外服务（客户端连接）
	wsAddr := fmt.Sprintf(":%d", cfg.WSServer.Port)
	go func() {
		logger.L.Sugar().Infof("WS server starting on %s", wsAddr)
		if err := http.ListenAndServe(wsAddr, wsMux); err != nil {
			log.Fatalf("start ws server failed: %v", err)
		}
	}()

	// 启动内部 Push 接口（供 Push 服务调用）
	internalMux := http.NewServeMux()
	internalMux.HandleFunc("/internal/push", server.HandleInternalPush)

	rpcAddr := fmt.Sprintf(":%d", cfg.WSServer.RPCPort)
	logger.L.Sugar().Infof("WS internal RPC server starting on %s", rpcAddr)
	if err := http.ListenAndServe(rpcAddr, internalMux); err != nil {
		log.Fatalf("start ws internal rpc server failed: %v", err)
	}
}
