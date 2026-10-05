package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/config"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/internal/ws"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type wsNotificationUserStub struct{ calls atomic.Int32 }

func (s *wsNotificationUserStub) CheckTeamMember(context.Context, *userpb.CheckTeamMemberRequest, ...grpc.CallOption) (*userpb.CheckTeamMemberResponse, error) {
	s.calls.Add(1)
	return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
}

type wsNotificationCloser struct{ calls atomic.Int32 }

func (c *wsNotificationCloser) Close() error {
	c.calls.Add(1)
	return nil
}

func wsNotificationCertificates(t *testing.T) (rpcauth.CertificateFiles, rpcauth.CertificateFiles) {
	t.Helper()
	dir := t.TempDir()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		Subject: pkix.Name{CommonName: "Task notification test CA"},
		IsCA:    true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		t.Fatal(err)
	}
	issue := func(name string, usage x509.ExtKeyUsage, serial int64) rpcauth.CertificateFiles {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{
			SerialNumber: big.NewInt(serial), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			Subject:  pkix.Name{CommonName: name},
			DNSNames: []string{name}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		files := rpcauth.CertificateFiles{CertFile: filepath.Join(dir, name+".pem"), KeyFile: filepath.Join(dir, name+".key"), CAFile: caPath}
		if err := os.WriteFile(files.CertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(files.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
			t.Fatal(err)
		}
		return files
	}
	return issue("ws.runtime.internal", x509.ExtKeyUsageServerAuth, 2), issue("push.runtime.internal", x509.ExtKeyUsageClientAuth, 3)
}

func wsNotificationConfig(files rpcauth.CertificateFiles) *config.Config {
	return &config.Config{
		WSServer: config.WSServerConfig{Port: 9090, RPCPort: 9091},
		TaskNotifications: config.TaskNotificationsConfig{WS: config.TaskNotificationWSConfig{
			Enabled: true, ListenAddr: "127.0.0.1:9443", PushDNSName: "push.runtime.internal", UserRPCAddr: "127.0.0.1:9001",
			TLS: config.NotificationTLSConfig{CertFile: files.CertFile, KeyFile: files.KeyFile, CAFile: files.CAFile},
		}},
	}
}

func TestPrepareTaskNotificationWSDisabledAndFactoryOrder(t *testing.T) {
	cfg := &config.Config{}
	calls := 0
	factories := taskNotificationWSFactories{
		tlsConfig: func(rpcauth.CertificateFiles, string) (*tls.Config, error) { calls++; return nil, nil },
		user:      func(string) (ws.TaskNotificationTeamChecker, io.Closer, error) { calls++; return nil, nil, nil },
		listen:    func(string, string) (net.Listener, error) { calls++; return nil, nil },
	}
	runtime, err := prepareTaskNotificationWS(cfg, nil, nil, factories)
	if runtime != nil || err != nil || calls != 0 {
		t.Fatalf("disabled WS allocated resources: runtime=%v, err=%v, calls=%d", runtime, err, calls)
	}
	cfg.TaskNotifications.WS.Enabled = true
	if runtime, err = prepareTaskNotificationWS(cfg, ws.NewHub(zap.NewNop()), nil, factories); runtime != nil || !errors.Is(err, errTaskNotificationWSConfig) || calls != 0 {
		t.Fatalf("invalid config touched resources: runtime=%v, err=%v, calls=%d", runtime, err, calls)
	}
	serverFiles, _ := wsNotificationCertificates(t)
	cfg = wsNotificationConfig(serverFiles)
	closer := &wsNotificationCloser{}
	order := []string{}
	factories.tlsConfig = func(rpcauth.CertificateFiles, string) (*tls.Config, error) {
		order = append(order, "tls")
		return nil, errors.New("private certificate path")
	}
	factories.user = func(string) (ws.TaskNotificationTeamChecker, io.Closer, error) {
		order = append(order, "user")
		return &wsNotificationUserStub{}, closer, errors.New("private User address")
	}
	if runtime, err = prepareTaskNotificationWS(cfg, ws.NewHub(zap.NewNop()), nil, factories); runtime != nil || !errors.Is(err, errTaskNotificationWSTLS) || len(order) != 1 || order[0] != "tls" || strings.Contains(err.Error(), "private") {
		t.Fatalf("TLS preflight did not precede User: runtime=%v, err=%v, order=%v", runtime, err, order)
	}
	factories.tlsConfig = rpcauth.NewNotificationServerTLSConfig
	if runtime, err = prepareTaskNotificationWS(cfg, ws.NewHub(zap.NewNop()), nil, factories); runtime != nil || !errors.Is(err, errTaskNotificationWSUser) || closer.calls.Load() != 1 || len(order) != 3 || order[1] != "tls" || order[2] != "user" || strings.Contains(err.Error(), "private") {
		t.Fatalf("partial User resource not closed: runtime=%v, err=%v, order=%v", runtime, err, order)
	}
}

func TestTaskNotificationWSRuntimeServesDedicatedTLSAndCloses(t *testing.T) {
	serverFiles, pushFiles := wsNotificationCertificates(t)
	cfg := wsNotificationConfig(serverFiles)
	checker := &wsNotificationUserStub{}
	closer := &wsNotificationCloser{}
	var listener net.Listener
	runtime, err := prepareTaskNotificationWS(cfg, ws.NewHub(zap.NewNop()), nil, taskNotificationWSFactories{
		user: func(string) (ws.TaskNotificationTeamChecker, io.Closer, error) { return checker, closer, nil },
		listen: func(network, addr string) (net.Listener, error) {
			if network != "tcp" || addr != cfg.TaskNotifications.WS.ListenAddr {
				return nil, errors.New("wrong dedicated address")
			}
			var listenErr error
			listener, listenErr = net.Listen("tcp", "127.0.0.1:0")
			return listener, listenErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(); !errors.Is(err, errTaskNotificationWSStart) {
		t.Fatalf("second Start succeeded: %v", err)
	}
	clientTLS, err := rpcauth.NewNotificationClientTLSConfig(pushFiles, "ws.runtime.internal")
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{TLSClientConfig: clientTLS}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	event := model.TaskNotificationEvent{Version: model.TaskNotificationEventVersion, Type: model.TaskNotificationEventType,
		NotificationID: 9007199254740997, TeamID: 9007199254740993, RecipientID: 9007199254740995}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "https://"+listener.Addr().String()+model.TaskNotificationPushPath, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var delivery model.TaskNotificationDelivery
	if err := json.NewDecoder(response.Body).Decode(&delivery); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || delivery.NotificationID != event.NotificationID || delivery.Outcome != model.TaskNotificationOffline || checker.calls.Load() != 0 {
		t.Fatalf("dedicated TLS offline result: status=%d delivery=%+v calls=%d", response.StatusCode, delivery, checker.calls.Load())
	}
	plainClient := &http.Client{Timeout: time.Second}
	plain, plainErr := plainClient.Get("http://" + listener.Addr().String() + model.TaskNotificationPushPath)
	if plain != nil {
		defer plain.Body.Close()
	}
	if plainErr == nil && plain.StatusCode == http.StatusOK || checker.calls.Load() != 0 {
		t.Fatalf("ordinary HTTP entered notification handler: response=%v err=%v", plain, plainErr)
	}
	if err := runtime.Close(); err != nil || closer.calls.Load() != 1 {
		t.Fatalf("runtime close: err=%v User closes=%d", err, closer.calls.Load())
	}
	if err := runtime.Close(); err != nil || closer.calls.Load() != 1 {
		t.Fatalf("runtime close not idempotent: err=%v User closes=%d", err, closer.calls.Load())
	}
	select {
	case err := <-runtime.Errors():
		t.Fatalf("normal shutdown reported listener failure: %v", err)
	default:
	}
}

func TestTaskNotificationWSRuntimeReportsBindAndAsyncServeFailures(t *testing.T) {
	serverFiles, _ := wsNotificationCertificates(t)
	cfg := wsNotificationConfig(serverFiles)
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	cfg.TaskNotifications.WS.ListenAddr = held.Addr().String()
	closer := &wsNotificationCloser{}
	newRuntime := func(listen func(string, string) (net.Listener, error)) *taskNotificationWSRuntime {
		t.Helper()
		runtime, err := prepareTaskNotificationWS(cfg, ws.NewHub(zap.NewNop()), nil, taskNotificationWSFactories{
			user: func(string) (ws.TaskNotificationTeamChecker, io.Closer, error) {
				return &wsNotificationUserStub{}, closer, nil
			},
			listen: listen,
		})
		if err != nil {
			t.Fatal(err)
		}
		return runtime
	}
	runtime := newRuntime(nil)
	if err := runtime.Start(); !errors.Is(err, errTaskNotificationWSStart) || strings.Contains(err.Error(), cfg.TaskNotifications.WS.ListenAddr) {
		t.Fatalf("occupied port did not fail synchronously: %v", err)
	}
	if err := runtime.Close(); err != nil || closer.calls.Load() != 1 {
		t.Fatalf("failed Start did not clean User: err=%v calls=%d", err, closer.calls.Load())
	}
	runtime = newRuntime(func(string, string) (net.Listener, error) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		_ = listener.Close()
		return listener, nil
	})
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-runtime.Errors():
		if !errors.Is(err, errTaskNotificationWSServe) {
			t.Fatalf("unsafe async listener error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("asynchronous listener failure was not reported")
	}
	if err := runtime.Close(); err != nil || closer.calls.Load() != 2 {
		t.Fatalf("failed Serve did not clean User: err=%v calls=%d", err, closer.calls.Load())
	}
}

func TestTaskNotificationWSRuntimeCancelsActiveHandlerBeforeUserClose(t *testing.T) {
	serverFiles, _ := wsNotificationCertificates(t)
	closer := &wsNotificationCloser{}
	runtime, err := prepareTaskNotificationWS(wsNotificationConfig(serverFiles), ws.NewHub(zap.NewNop()), nil, taskNotificationWSFactories{
		user: func(string) (ws.TaskNotificationTeamChecker, io.Closer, error) {
			return &wsNotificationUserStub{}, closer, nil
		},
		listen: func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if err := runtime.Start(); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	finished := make(chan bool, 1)
	requestContext := runtime.server.BaseContext(nil)
	go runtime.serveNotification(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		finished <- closer.calls.Load() == 0
	}), httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, model.TaskNotificationPushPath, nil).WithContext(requestContext))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("active handler did not begin")
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case dependencyStayedOpen := <-finished:
		if !dependencyStayedOpen || closer.calls.Load() != 1 {
			t.Fatalf("User closed before active handler exited: stayed=%v closes=%d", dependencyStayedOpen, closer.calls.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("active handler was not canceled")
	}
}
