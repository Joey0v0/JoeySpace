package main

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/yjydist/go-im/internal/config"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/internal/ws"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	errTaskNotificationWSConfig   = errors.New("invalid task notification WS configuration")
	errTaskNotificationWSTLS      = errors.New("task notification WS TLS preparation failed")
	errTaskNotificationWSUser     = errors.New("task notification WS User client preparation failed")
	errTaskNotificationWSStart    = errors.New("task notification WS listener start failed")
	errTaskNotificationWSServe    = errors.New("task notification WS listener stopped unexpectedly")
	errTaskNotificationWSShutdown = errors.New("task notification WS shutdown incomplete")
	errTaskNotificationWSClose    = errors.New("task notification WS User client close failed")
)

type taskNotificationWSFactories struct {
	tlsConfig func(rpcauth.CertificateFiles, string) (*tls.Config, error)
	user      func(string) (ws.TaskNotificationTeamChecker, io.Closer, error)
	listen    func(string, string) (net.Listener, error)
}

type taskNotificationWSRuntime struct {
	config     config.TaskNotificationWSConfig
	hub        *ws.Hub
	logger     *zap.Logger
	tlsConfig  *tls.Config
	user       ws.TaskNotificationTeamChecker
	userCloser io.Closer
	listen     func(string, string) (net.Listener, error)
	errors     chan error

	mu          sync.Mutex
	started     bool
	closed      bool
	server      *http.Server
	serveDone   chan struct{}
	cancelBase  context.CancelFunc
	activeMu    sync.Mutex
	stopping    bool
	active      int
	activeDone  chan struct{}
	closeOnce   sync.Once
	closeResult error
}

func prepareTaskNotificationWS(cfg *config.Config, hub *ws.Hub, logger *zap.Logger, factories taskNotificationWSFactories) (*taskNotificationWSRuntime, error) {
	if cfg == nil {
		return nil, errTaskNotificationWSConfig
	}
	if !cfg.TaskNotifications.WS.Enabled {
		return nil, nil
	}
	if config.ValidateTaskNotificationWS(cfg) != nil || hub == nil {
		return nil, errTaskNotificationWSConfig
	}
	if factories.tlsConfig == nil {
		factories.tlsConfig = rpcauth.NewNotificationServerTLSConfig
	}
	if factories.user == nil {
		factories.user = func(addr string) (ws.TaskNotificationTeamChecker, io.Closer, error) {
			conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				return nil, nil, err
			}
			return userpb.NewUserClient(conn), conn, nil
		}
	}
	if factories.listen == nil {
		factories.listen = net.Listen
	}
	c := cfg.TaskNotifications.WS
	tlsConfig, err := factories.tlsConfig(rpcauth.CertificateFiles{
		CertFile: c.TLS.CertFile, KeyFile: c.TLS.KeyFile, CAFile: c.TLS.CAFile,
	}, c.PushDNSName)
	if err != nil || tlsConfig == nil || tlsConfig.MinVersion < tls.VersionTLS13 || len(tlsConfig.Certificates) == 0 ||
		tlsConfig.ClientAuth != tls.RequireAndVerifyClientCert || tlsConfig.ClientCAs == nil || tlsConfig.VerifyConnection == nil {
		return nil, errTaskNotificationWSTLS
	}
	user, closer, err := factories.user(c.UserRPCAddr)
	if err != nil || user == nil || closer == nil {
		if closer != nil {
			_ = closer.Close()
		}
		return nil, errTaskNotificationWSUser
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	activeDone := make(chan struct{})
	close(activeDone)
	return &taskNotificationWSRuntime{
		config: c, hub: hub, logger: logger, tlsConfig: tlsConfig.Clone(), user: user, userCloser: closer,
		listen: factories.listen, errors: make(chan error, 1), activeDone: activeDone,
	}, nil
}

func (r *taskNotificationWSRuntime) Start() error {
	if r == nil {
		return errTaskNotificationWSStart
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.closed {
		return errTaskNotificationWSStart
	}
	r.started = true
	listener, err := r.listen("tcp", r.config.ListenAddr)
	if err != nil || listener == nil {
		return errTaskNotificationWSStart
	}
	baseCtx, cancel := context.WithCancel(context.Background())
	r.cancelBase = cancel
	mux := http.NewServeMux()
	mux.Handle(model.TaskNotificationPushPath, ws.NewTaskNotificationHandler(r.hub, r.user, r.config.PushDNSName))
	r.server = &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) { r.serveNotification(mux, w, request) }),
		TLSConfig:         r.tlsConfig.Clone(),
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	r.serveDone = make(chan struct{})
	go func() {
		_ = r.server.ServeTLS(listener, "", "")
		r.mu.Lock()
		closing := r.closed
		r.mu.Unlock()
		if !closing {
			r.logger.Error("task notification WS listener stopped unexpectedly")
			select {
			case r.errors <- errTaskNotificationWSServe:
			default:
			}
		}
		close(r.serveDone)
	}()
	return nil
}

func (r *taskNotificationWSRuntime) serveNotification(handler http.Handler, w http.ResponseWriter, request *http.Request) {
	r.activeMu.Lock()
	if r.stopping {
		r.activeMu.Unlock()
		http.Error(w, "task notification delivery unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.active == 0 {
		r.activeDone = make(chan struct{})
	}
	r.active++
	r.activeMu.Unlock()
	defer func() {
		r.activeMu.Lock()
		r.active--
		if r.active == 0 {
			close(r.activeDone)
		}
		r.activeMu.Unlock()
	}()
	handler.ServeHTTP(w, request)
}

func (r *taskNotificationWSRuntime) Errors() <-chan error {
	if r == nil {
		return nil
	}
	return r.errors
}

func (r *taskNotificationWSRuntime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		server, serveDone, cancelBase, closer := r.server, r.serveDone, r.cancelBase, r.userCloser
		r.mu.Unlock()
		r.activeMu.Lock()
		r.stopping = true
		activeDone := r.activeDone
		r.activeMu.Unlock()
		if server != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			shutdownErr := server.Shutdown(shutdownCtx)
			cancel()
			if cancelBase != nil {
				cancelBase()
			}
			if shutdownErr != nil {
				r.closeResult = errTaskNotificationWSShutdown
				_ = server.Close()
			}
			<-serveDone
			select {
			case <-activeDone:
			case <-time.After(5 * time.Second):
				// Keep User alive until a stuck handler actually exits.
				go func() {
					<-activeDone
					if closer != nil {
						_ = closer.Close()
					}
				}()
				r.closeResult = errTaskNotificationWSShutdown
				return
			}
		}
		if cancelBase != nil {
			cancelBase()
		}
		if closer != nil && closer.Close() != nil && r.closeResult == nil {
			r.closeResult = errTaskNotificationWSClose
		}
	})
	return r.closeResult
}
