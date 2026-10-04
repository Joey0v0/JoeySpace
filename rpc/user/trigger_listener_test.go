package main

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func triggerListenerConfig(t *testing.T) userTriggerConfig {
	t.Helper()
	serverFiles, _, _ := triggerTestCertificates(t)
	return userTriggerConfig{ListenOn: "127.0.0.1:0", IMDNSName: "im.go-im.internal", Files: serverFiles}
}

func triggerListenerEnv() map[string]string {
	return map[string]string{
		"USER_TRIGGER_LISTEN_ON": "0.0.0.0:9005", "USER_TRIGGER_IM_DNS_NAME": "im.go-im.internal",
		"USER_TRIGGER_TLS_CERT_FILE": "server.pem", "USER_TRIGGER_TLS_KEY_FILE": "server.key",
		"USER_TRIGGER_TLS_CA_FILE": "ca.pem",
	}
}

func TestUserTriggerConfiguration(t *testing.T) {
	c, err := loadUserTriggerConfig(func(string) string { return "" })
	if err != nil || !c.disabled() {
		t.Fatalf("all-empty config: %+v, %v", c, err)
	}
	values := triggerListenerEnv()
	c, err = loadUserTriggerConfig(func(key string) string { return values[key] })
	if err != nil || c.ListenOn != values["USER_TRIGGER_LISTEN_ON"] || c.IMDNSName != values["USER_TRIGGER_IM_DNS_NAME"] ||
		c.Files.CertFile != "server.pem" || c.Files.KeyFile != "server.key" || c.Files.CAFile != "ca.pem" {
		t.Fatalf("complete config: %+v, %v", c, err)
	}
	for key := range values {
		for _, value := range []string{"", " ", "\t", "\n"} {
			t.Run(key+"/missing-or-blank/"+value, func(t *testing.T) {
				env := triggerListenerEnv()
				env[key] = value
				if _, err := loadUserTriggerConfig(func(key string) string { return env[key] }); err == nil {
					t.Fatal("accepted partial or blank configuration")
				}
			})
		}
		t.Run(key+"/only-field", func(t *testing.T) {
			if _, err := loadUserTriggerConfig(func(name string) string {
				if name == key {
					return values[key]
				}
				return ""
			}); err == nil {
				t.Fatal("accepted single configured field")
			}
		})
	}
}

func TestUserTriggerConfigurationRejectsInvalidAddressesAndDNS(t *testing.T) {
	for _, addr := range []string{"localhost", "127.0.0.1:0", "127.0.0.1:-1", "127.0.0.1:+80", "127.0.0.1:65536",
		"127.0.0.1:abc", "tcp://127.0.0.1:9005", " localhost:9005", "localhost:9005 ", "local host:9005", "*:9005", "[::1]:"} {
		t.Run(addr, func(t *testing.T) {
			env := triggerListenerEnv()
			env["USER_TRIGGER_LISTEN_ON"] = addr
			if _, err := loadUserTriggerConfig(func(key string) string { return env[key] }); err == nil {
				t.Fatal("accepted invalid listen address")
			}
		})
	}
	for _, name := range []string{"*.go-im.internal", "im .go-im.internal", "im\t.go-im.internal", "im\u2003.go-im.internal",
		" im.go-im.internal", "im.go-im.internal ", "im/go-im", "im:9005", "im..internal", "-im.internal"} {
		t.Run(name, func(t *testing.T) {
			env := triggerListenerEnv()
			env["USER_TRIGGER_IM_DNS_NAME"] = name
			if _, err := loadUserTriggerConfig(func(key string) string { return env[key] }); err == nil {
				t.Fatal("accepted invalid IM identity")
			}
		})
	}
	for _, addr := range []string{":1", "localhost:65535", "127.0.0.1:9005", "[::1]:9005"} {
		t.Run("valid/"+addr, func(t *testing.T) {
			env := triggerListenerEnv()
			env["USER_TRIGGER_LISTEN_ON"] = addr
			if _, err := loadUserTriggerConfig(func(key string) string { return env[key] }); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUserTriggerStartupRequiresProfileAndSeparatePort(t *testing.T) {
	if err := validateUserTriggerStartup(userTriggerConfig{}, false, "unused"); err != nil {
		t.Fatal("disabled trigger changed ordinary startup:", err)
	}
	env := triggerListenerEnv()
	c, err := loadUserTriggerConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if err := validateUserTriggerStartup(c, false, "127.0.0.1:9001"); err == nil {
		t.Fatal("enabled trigger accepted demo mode")
	}
	for _, addr := range []string{"127.0.0.1:9005", ":9005", "[::1]:9005"} {
		if err := validateUserTriggerStartup(c, true, addr); err == nil {
			t.Fatalf("accepted conflicting ordinary port %s", addr)
		}
	}
	if err := validateUserTriggerStartup(c, true, "127.0.0.1:9001"); err != nil {
		t.Fatal(err)
	}
}

func TestUserTriggerRuntimeConfigurationAndDatabaseGuards(t *testing.T) {
	r, err := newUserTriggerRuntime(userTriggerConfig{}, nil)
	if err != nil || r != nil {
		t.Fatalf("disabled runtime: %v, %v", r, err)
	}
	r.Stop()
	c := triggerListenerConfig(t)
	for _, users := range []*userServer{nil, {}} {
		if r, err := newUserTriggerRuntime(c, users); err == nil || r != nil {
			t.Fatal("enabled listener accepted missing database")
		}
	}
	users, _ := newTestUserServer(t)
	partial := c
	partial.ListenOn = ""
	if r, err := newUserTriggerRuntime(partial, users); err == nil || r != nil {
		t.Fatal("constructor silently disabled partial configuration")
	}
	badTLS := c
	badTLS.Files.KeyFile = "missing-user-trigger-test-key"
	if r, err := newUserTriggerRuntime(badTLS, users); err == nil || r != nil {
		t.Fatal("constructor accepted unavailable credentials")
	}
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	c.ListenOn = occupied.Addr().String()
	if r, err := newUserTriggerRuntime(c, users); err == nil || r != nil {
		t.Fatal("constructor accepted occupied listen port")
	}
}

func startTriggerListenerTest(t *testing.T) (*userTriggerRuntime, rpcauth.CertificateFiles, rpcauth.CertificateFiles) {
	t.Helper()
	serverFiles, imFiles, agentFiles := triggerTestCertificates(t)
	users, _ := newTestUserServer(t)
	users.jwtSecret = "" // The service channel does not authenticate with JWT.
	r, err := newUserTriggerRuntime(userTriggerConfig{ListenOn: "127.0.0.1:0", IMDNSName: "im.go-im.internal", Files: serverFiles}, users)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.server.Serve(r.listener)
	}()
	t.Cleanup(func() { r.Stop(); <-done })
	return r, imFiles, agentFiles
}

func triggerListenerConn(t *testing.T, addr string, creds credentials.TransportCredentials) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestUserTriggerListenerOnlyRegistersDedicatedService(t *testing.T) {
	r, imFiles, _ := startTriggerListenerTest(t)
	info := r.server.GetServiceInfo()
	service, ok := info["user.UserTrigger"]
	if !ok || len(info) != 1 || len(service.Methods) != 1 || service.Methods[0].Name != "CheckTriggerTeamMember" {
		t.Fatalf("unexpected services: %+v", info)
	}
	creds, err := rpcauth.NewServiceClientCredentials(imFiles, "user.go-im.internal")
	if err != nil {
		t.Fatal(err)
	}
	conn := triggerListenerConn(t, r.listener.Addr().String(), creds)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = pb.NewUserClient(conn).GetUserInfo(ctx, &pb.GetUserInfoRequest{UserId: 1})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("ordinary User exposed or trusted IM handshake failed: %v", err)
	}
}

func TestUserTriggerListenerRejectsAgentAndPlaintext(t *testing.T) {
	r, _, agentFiles := startTriggerListenerTest(t)
	agentCreds, err := rpcauth.NewServiceClientCredentials(agentFiles, "user.go-im.internal")
	if err != nil {
		t.Fatal(err)
	}
	for name, creds := range map[string]credentials.TransportCredentials{"same-CA Agent": agentCreds, "plaintext": insecure.NewCredentials()} {
		t.Run(name, func(t *testing.T) {
			conn := triggerListenerConn(t, r.listener.Addr().String(), creds)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := pb.NewUserClient(conn).GetUserInfo(ctx, &pb.GetUserInfoRequest{UserId: 1})
			// An authenticated connection would get Unimplemented on this dedicated port.
			if status.Code(err) != codes.Unavailable && status.Code(err) != codes.DeadlineExceeded {
				t.Fatalf("untrusted connection was not rejected by TLS: %v", err)
			}
		})
	}
}

func TestUserTriggerListenerLimitsRequestSize(t *testing.T) {
	r, imFiles, _ := startTriggerListenerTest(t)
	creds, err := rpcauth.NewServiceClientCredentials(imFiles, "user.go-im.internal")
	if err != nil {
		t.Fatal(err)
	}
	conn := triggerListenerConn(t, r.listener.Addr().String(), creds)
	req := &pb.CheckTriggerTeamMemberRequest{ActorId: 42, TeamId: 7}
	// Valid unknown field 3, repeated, pushes the protobuf message above 4096.
	req.ProtoReflect().SetUnknown(bytes.Repeat([]byte{0x18, 0x01}, 2050))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = pb.NewUserTriggerClient(conn).CheckTriggerTeamMember(ctx, req)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversized request bypassed listener guard: %v", err)
	}
}

func TestUserTriggerRuntimeStopIsConcurrentAndIdempotent(t *testing.T) {
	r, _, _ := startTriggerListenerTest(t)
	addr := r.listener.Addr().String()
	var calls sync.WaitGroup
	for range 5 {
		calls.Add(1)
		go func() {
			defer calls.Done()
			r.Stop()
		}()
	}
	calls.Wait()
	r.Stop()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("Stop left the listening port open")
	}
}

func TestUserTriggerFailureStopsOrdinaryServerWithoutWaitingForFramework(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ordinary := grpc.NewServer()
	pb.RegisterUserServer(ordinary, &userServer{})
	ready := make(chan *grpc.Server, 1)
	failed := make(chan struct{}, 1)
	frameworkWaiting := make(chan struct{})
	releaseShutdown := make(chan struct{})
	startExited := make(chan struct{})
	helperExited := make(chan struct{})
	result := make(chan bool, 1)
	serveError := make(chan error, 1)
	t.Cleanup(func() {
		ordinary.Stop()
		_ = listener.Close()
		close(releaseShutdown)
		for _, done := range []<-chan struct{}{startExited, helperExited} {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("listener wait test left a goroutine running")
			}
		}
	})
	go func() {
		defer close(helperExited)
		result <- waitUserTriggerServers(func() {
			defer close(startExited)
			ready <- ordinary
			serveError <- ordinary.Serve(listener)
			close(frameworkWaiting)
			// Model Linux go-zero's shutdown wait after its actual Serve exits.
			<-releaseShutdown
		}, ready, failed)
	}()
	conn := triggerListenerConn(t, listener.Addr().String(), insecure.NewCredentials())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	response, err := pb.NewUserClient(conn).GetUserInfo(ctx, &pb.GetUserInfoRequest{UserId: 1})
	cancel()
	if err != nil || response == nil {
		t.Fatalf("ordinary service was not running before dedicated failure: %v", err)
	}
	failed <- struct{}{}
	select {
	case stopped := <-result:
		if !stopped {
			t.Fatal("dedicated failure reported normal ordinary completion")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("helper blocked waiting for framework shutdown notification")
	}
	select {
	case <-frameworkWaiting:
	case <-time.After(time.Second):
		t.Fatal("ordinary Serve was not stopped")
	}
	if err := <-serveError; err != nil {
		t.Fatalf("ordinary Serve did not stop normally: %v", err)
	}
	select {
	case <-startExited:
		t.Fatal("test did not retain the framework shutdown wait")
	default:
	}
	ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = pb.NewUserClient(conn).GetUserInfo(ctx, &pb.GetUserInfoRequest{UserId: 1})
	if status.Code(err) != codes.Unavailable && status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("ordinary server remained callable after helper returned: %v", err)
	}
}

func TestUserTriggerWaitPropagatesStartPanicToCallerCleanup(t *testing.T) {
	const originalPanic = "ordinary startup failed"
	var recovered any
	cleaned := false
	func() {
		defer func() { recovered = recover() }()
		defer func() { cleaned = true }()
		waitUserTriggerServers(func() { panic(originalPanic) }, nil, nil)
	}()
	if recovered != originalPanic || !cleaned {
		t.Fatalf("startup panic lost caller cleanup: recovered=%v cleaned=%t", recovered, cleaned)
	}
	if stopped := waitUserTriggerServers(func() {}, nil, nil); stopped {
		t.Fatal("normal ordinary completion reported dedicated failure")
	}
}
