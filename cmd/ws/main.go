package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/yjydist/go-im/internal/config"
	"github.com/yjydist/go-im/internal/pkg/logger"
	"github.com/yjydist/go-im/internal/repository"
	"github.com/yjydist/go-im/internal/ws"
)

func main() {
	if err := runWS(); err != nil {
		log.Fatal(err)
	}
}

func runWS() error {
	configPath := flag.String("config", "config/go-im.yaml", "config file path")
	flag.Parse()

	// 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config failed: %w", err)
	}
	if err := config.ValidateTaskNotificationWS(cfg); err != nil {
		return err
	}

	// 初始化日志
	if err := logger.Init(cfg.Log.Level, cfg.Log.Filename); err != nil {
		return fmt.Errorf("init logger failed: %w", err)
	}
	defer logger.Sync()

	// Prepare credentials before Redis, and do not bind the optional TLS port yet.
	hub := ws.NewHub(logger.L)
	notifications, err := prepareTaskNotificationWS(cfg, hub, logger.L, taskNotificationWSFactories{})
	if err != nil {
		return err
	}
	if notifications != nil {
		defer notifications.Close()
	}

	// 初始化 Redis（WS 网关需要 Redis 来管理在线状态和消息去重）
	if err := repository.InitRedis(&cfg.Redis, logger.L); err != nil {
		return fmt.Errorf("init redis failed: %w", err)
	}

	// 创建 Hub 并启动
	go hub.Run()

	// 创建 RedisRepo
	redisRepo := repository.NewRedisRepository()

	// 创建 WS Server
	server := ws.NewServer(cfg, hub, redisRepo, logger.L)
	defer server.Close()

	// 注册 WebSocket 路由
	wsMux := http.NewServeMux()
	wsMux.HandleFunc("/ws", server.HandleWS)
	wsMux.HandleFunc("/ws-ticket", server.HandleWSTicket)

	// 启动 WS 对外服务（客户端连接）
	wsAddr := fmt.Sprintf(":%d", cfg.WSServer.Port)

	// 启动内部 Push 接口（供 Push 服务调用）
	internalMux := http.NewServeMux()
	internalMux.HandleFunc("/internal/push", server.HandleInternalPush)

	rpcAddr := fmt.Sprintf(":%d", cfg.WSServer.RPCPort)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return serveWSHTTP(ctx,
		&http.Server{Addr: wsAddr, Handler: wsMux, ReadHeaderTimeout: 5 * time.Second},
		&http.Server{Addr: rpcAddr, Handler: internalMux, ReadHeaderTimeout: 5 * time.Second}, notifications)
}

// Bind both existing HTTP ports synchronously. The optional notification server
// owns its separate TLS listener. Normal signal shutdown is not a serve failure.
func serveWSHTTP(ctx context.Context, public, internal *http.Server, notifications *taskNotificationWSRuntime) (resultErr error) {
	if ctx == nil || public == nil || internal == nil {
		return errors.New("WS HTTP startup dependencies required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	publicListener, err := net.Listen("tcp", public.Addr)
	if err != nil {
		return errors.New("cannot bind WS public listener")
	}
	defer publicListener.Close()
	internalListener, err := net.Listen("tcp", internal.Addr)
	if err != nil {
		return errors.New("cannot bind WS internal listener")
	}
	defer internalListener.Close()
	if notifications != nil {
		if err := notifications.Start(); err != nil {
			return err
		}
	}
	var workers sync.WaitGroup
	errorsCh := make(chan error, 2)
	for _, item := range []struct {
		server   *http.Server
		listener net.Listener
	}{{public, publicListener}, {internal, internalListener}} {
		workers.Add(1)
		go func(server *http.Server, listener net.Listener) {
			defer workers.Done()
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errorsCh <- errors.New("WS HTTP listener stopped unexpectedly")
			}
		}(item.server, item.listener)
	}
	defer func() {
		for _, server := range []*http.Server{public, internal} {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := server.Shutdown(shutdownCtx)
			cancel()
			if err != nil {
				_ = server.Close()
				resultErr = errors.Join(resultErr, errors.New("WS HTTP shutdown timed out"))
			}
		}
		workers.Wait()
		if notifications != nil {
			resultErr = errors.Join(resultErr, notifications.Close())
		}
	}()
	var notificationErrors <-chan error
	if notifications != nil {
		notificationErrors = notifications.Errors()
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-errorsCh:
		return err
	case err := <-notificationErrors:
		return err
	}
}
