package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type triggerListenerTeamsFunc func(context.Context, int64, int64) error

func (f triggerListenerTeamsFunc) Check(ctx context.Context, actor, team int64) error {
	return f(ctx, actor, team)
}

func imTriggerListenerEnv() map[string]string {
	return map[string]string{
		"IM_TRIGGER_LISTEN_ON": "0.0.0.0:9006", "IM_TRIGGER_AGENT_DNS_NAME": "agent.go-im.internal",
		"IM_TRIGGER_TLS_CERT_FILE": "server.pem", "IM_TRIGGER_TLS_KEY_FILE": "server.key", "IM_TRIGGER_TLS_CA_FILE": "ca.pem",
	}
}

func TestIMTriggerConfigDisabledAndPartialFailClosed(t *testing.T) {
	c, err := loadIMTriggerConfig(func(string) string { return "" })
	if err != nil || !c.disabled() {
		t.Fatalf("disabled config: %+v %v", c, err)
	}
	if _, err := loadIMTriggerConfig(nil); err == nil {
		t.Fatal("nil environment reader accepted")
	}
	env := imTriggerListenerEnv()
	c, err = loadIMTriggerConfig(func(key string) string { return env[key] })
	if err != nil || c.ListenOn != env["IM_TRIGGER_LISTEN_ON"] || c.AgentDNSName != env["IM_TRIGGER_AGENT_DNS_NAME"] ||
		c.Files.CertFile != "server.pem" || c.Files.KeyFile != "server.key" || c.Files.CAFile != "ca.pem" {
		t.Fatalf("complete config: %+v %v", c, err)
	}
	for key := range env {
		for _, value := range []string{"", " ", "\t", "\n", " " + env[key]} {
			t.Run(key+"/bad/"+value, func(t *testing.T) {
				values := imTriggerListenerEnv()
				values[key] = value
				if _, err := loadIMTriggerConfig(func(key string) string { return values[key] }); err == nil {
					t.Fatal("accepted missing or blank configuration")
				}
			})
		}
		if _, err := loadIMTriggerConfig(func(name string) string {
			if name == key {
				return env[key]
			}
			return ""
		}); err == nil {
			t.Fatalf("accepted only %s configured", key)
		}
	}
}

func TestIMTriggerConfigRejectsInvalidAddressDNSAndConflicts(t *testing.T) {
	for _, addr := range []string{"localhost", "127.0.0.1:0", "127.0.0.1:-1", "127.0.0.1:+90", "127.0.0.1:65536", "127.0.0.1:abc",
		"tcp://127.0.0.1:9006", " localhost:9006", "localhost:9006 ", "local host:9006", "*:9006", "[::1]:"} {
		env := imTriggerListenerEnv()
		env["IM_TRIGGER_LISTEN_ON"] = addr
		if _, err := loadIMTriggerConfig(func(key string) string { return env[key] }); err == nil {
			t.Fatalf("accepted invalid address %q", addr)
		}
	}
	for _, name := range []string{"*.go-im.internal", "agent .internal", "agent\t.internal", "agent\u2003.internal", "agent/internal", "agent:9006", "agent..internal", "-agent.internal"} {
		env := imTriggerListenerEnv()
		env["IM_TRIGGER_AGENT_DNS_NAME"] = name
		if _, err := loadIMTriggerConfig(func(key string) string { return env[key] }); err == nil {
			t.Fatalf("accepted invalid identity %q", name)
		}
	}
	for _, addr := range []string{":1", "localhost:65535", "[::1]:9006"} {
		env := imTriggerListenerEnv()
		env["IM_TRIGGER_LISTEN_ON"] = addr
		if _, err := loadIMTriggerConfig(func(key string) string { return env[key] }); err != nil {
			t.Fatalf("rejected valid address %q: %v", addr, err)
		}
	}
	env := imTriggerListenerEnv()
	c, _ := loadIMTriggerConfig(func(key string) string { return env[key] })
	for _, tc := range []struct{ ordinary, bot string }{
		{"127.0.0.1:9006", ""}, {":9006", "127.0.0.1:9005"}, {"127.0.0.1:9002", "[::1]:9006"}, {"", ""},
	} {
		if err := validateIMTriggerStartup(c, tc.ordinary, tc.bot); err == nil {
			t.Fatalf("accepted conflicting or invalid ports %+v", tc)
		}
	}
	if err := validateIMTriggerStartup(c, "127.0.0.1:9002", "127.0.0.1:9005"); err != nil {
		t.Fatal(err)
	}
	if err := validateIMTriggerStartup(c, "127.0.0.1:9002", ""); err != nil {
		t.Fatal(err)
	}
	if err := validateIMTriggerStartup(imTriggerConfig{}, "unused", "unused"); err != nil {
		t.Fatal("disabled trigger changed existing startup", err)
	}
}

func TestIMTriggerRequiresCompleteUserClientOnlyWhenEnabled(t *testing.T) {
	if _, err := loadIMTriggerTeamConfig(imTriggerConfig{}, func(string) string {
		t.Fatal("disabled listener read unused User client configuration")
		return ""
	}); err != nil {
		t.Fatal(err)
	}
	c := imTriggerConfig{ListenOn: "127.0.0.1:9006"}
	if _, err := loadIMTriggerTeamConfig(c, func(string) string { return "" }); err == nil {
		t.Fatal("enabled listener accepted disabled User client")
	}
	if _, err := loadIMTriggerTeamConfig(c, func(key string) string {
		if key == "IM_TRIGGER_USER_RPC_ADDR" {
			return "user-rpc:9004"
		}
		return ""
	}); err == nil {
		t.Fatal("enabled listener accepted partial User client")
	}
	want := validTriggerClientConfig()
	got, err := loadIMTriggerTeamConfig(c, triggerClientEnvironment(want))
	if err != nil || got != want {
		t.Fatalf("complete User client: %+v %v", got, err)
	}
}

func TestIMTriggerRuntimeGuardsDependenciesAndTLS(t *testing.T) {
	r, err := newIMTriggerRuntime(imTriggerConfig{}, nil, nil)
	if err != nil || r != nil {
		t.Fatalf("disabled runtime: %v %v", r, err)
	}
	r.Stop()
	files, _, _ := botTestCertificates(t)
	c := imTriggerConfig{ListenOn: "127.0.0.1:0", AgentDNSName: "agent.go-im.internal", Files: files}
	im, _ := testIMServer(t)
	teams := triggerListenerTeamsFunc(func(context.Context, int64, int64) error { return nil })
	if r, err := newIMTriggerRuntime(c, nil, teams); err == nil || r != nil {
		t.Fatal("enabled listener accepted missing DB")
	}
	for _, client := range []triggerTeamEligibility{nil, (*triggerTeamClient)(nil), &triggerTeamClient{}} {
		if r, err := newIMTriggerRuntime(c, im.db, client); err == nil || r != nil {
			t.Fatal("enabled listener accepted missing User client")
		}
	}
	partial := c
	partial.ListenOn = ""
	if r, err := newIMTriggerRuntime(partial, im.db, teams); err == nil || r != nil {
		t.Fatal("constructor disabled partial configuration")
	}
	badTLS := c
	badTLS.Files.KeyFile = "missing-im-trigger-test-key"
	if r, err := newIMTriggerRuntime(badTLS, im.db, teams); err == nil || r != nil {
		t.Fatal("constructor accepted unavailable credentials")
	}
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	c.ListenOn = occupied.Addr().String()
	if r, err := newIMTriggerRuntime(c, im.db, teams); err == nil || r != nil {
		t.Fatal("constructor accepted occupied port")
	}
}

func startIMTriggerListenerTest(t *testing.T) (*imTriggerRuntime, rpcauth.CertificateFiles, rpcauth.CertificateFiles) {
	t.Helper()
	serverFiles, agentFiles, otherFiles := botTestCertificates(t)
	im, _ := testIMServer(t)
	teams := triggerListenerTeamsFunc(func(context.Context, int64, int64) error { return errors.New("unexpected eligibility call") })
	r, err := newIMTriggerRuntime(imTriggerConfig{ListenOn: "127.0.0.1:0", AgentDNSName: "agent.go-im.internal", Files: serverFiles}, im.db, teams)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = r.server.Serve(r.listener) }()
	t.Cleanup(func() { r.Stop(); <-done })
	return r, agentFiles, otherFiles
}

func imTriggerListenerConn(t *testing.T, addr string, creds credentials.TransportCredentials) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestIMTriggerListenerOnlyExposesDedicatedServiceToAgent(t *testing.T) {
	r, agentFiles, _ := startIMTriggerListenerTest(t)
	info := r.server.GetServiceInfo()
	service, ok := info["im.IMTrigger"]
	methods := make(map[string]bool, len(service.Methods))
	for _, method := range service.Methods {
		methods[method.Name] = true
	}
	if !ok || len(info) != 1 || len(service.Methods) != 2 || !methods["ReadTaskTriggerContext"] || !methods["ResolveTaskTriggerMember"] {
		t.Fatalf("unexpected services: %+v", info)
	}
	creds, err := rpcauth.NewServiceClientCredentials(agentFiles, "im.go-im.internal")
	if err != nil {
		t.Fatal(err)
	}
	conn := imTriggerListenerConn(t, r.listener.Addr().String(), creds)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = pb.NewIMClient(conn).CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{GroupId: 300})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("ordinary IM exposed or trusted Agent handshake failed: %v", err)
	}
	_, err = pb.NewIMBotClient(conn).PostTaskCreatedCard(ctx, &pb.PostTaskCreatedCardRequest{RunId: 9})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("bot service exposed on dedicated read port: %v", err)
	}
}

func TestIMTriggerListenerRejectsOtherRoleAndPlaintext(t *testing.T) {
	r, _, otherFiles := startIMTriggerListenerTest(t)
	otherCreds, err := rpcauth.NewServiceClientCredentials(otherFiles, "im.go-im.internal")
	if err != nil {
		t.Fatal(err)
	}
	for name, creds := range map[string]credentials.TransportCredentials{"same-CA Task": otherCreds, "plaintext": insecure.NewCredentials()} {
		t.Run(name, func(t *testing.T) {
			conn := imTriggerListenerConn(t, r.listener.Addr().String(), creds)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := pb.NewIMClient(conn).CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{GroupId: 300})
			if status.Code(err) != codes.Unavailable && status.Code(err) != codes.DeadlineExceeded {
				t.Fatalf("untrusted connection passed TLS: %v", err)
			}
		})
	}
}

func TestIMTriggerListenerLimitsRequestSize(t *testing.T) {
	r, agentFiles, _ := startIMTriggerListenerTest(t)
	creds, err := rpcauth.NewServiceClientCredentials(agentFiles, "im.go-im.internal")
	if err != nil {
		t.Fatal(err)
	}
	conn := imTriggerListenerConn(t, r.listener.Addr().String(), creds)
	req := &pb.ReadTaskTriggerContextRequest{MessageId: 42}
	req.ProtoReflect().SetUnknown(bytes.Repeat([]byte{0x10, 0x01}, 2050))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = pb.NewIMTriggerClient(conn).ReadTaskTriggerContext(ctx, req)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversized request bypassed receive guard: %v", err)
	}
}

func TestIMTriggerRuntimeStopIdempotentDoesNotOwnDatabase(t *testing.T) {
	files, _, _ := botTestCertificates(t)
	im, mock := testIMServer(t)
	teams := triggerListenerTeamsFunc(func(context.Context, int64, int64) error { return nil })
	r, err := newIMTriggerRuntime(imTriggerConfig{ListenOn: "127.0.0.1:0", AgentDNSName: "agent.go-im.internal", Files: files}, im.db, teams)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	addr := r.listener.Addr().String()
	var calls sync.WaitGroup
	for range 5 {
		calls.Add(1)
		go func() { defer calls.Done(); r.Stop() }()
	}
	calls.Wait()
	r.Stop()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("Stop left listening port open")
	}
	mock.ExpectExec("SELECT 1").WillReturnResult(sqlmock.NewResult(0, 0))
	db, err := im.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), "SELECT 1"); err != nil {
		t.Fatal("runtime closed process-owned DB", err)
	}
}

func TestIMTriggerFailureStopsOrdinaryServerWithoutWaitingForFramework(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ordinary := grpc.NewServer()
	im, _ := testIMServer(t)
	registerOrdinaryIM(ordinary, im)
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
		result <- waitIMTriggerServers(func() {
			defer close(startExited)
			ready <- ordinary
			serveError <- ordinary.Serve(listener)
			close(frameworkWaiting)
			// Model Linux go-zero's shutdown wait after its actual Serve exits.
			<-releaseShutdown
		}, ready, failed)
	}()
	conn := imTriggerListenerConn(t, listener.Addr().String(), insecure.NewCredentials())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, err = pb.NewIMClient(conn).CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{})
	cancel()
	if status.Code(err) != codes.InvalidArgument {
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
	_, err = pb.NewIMClient(conn).CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{})
	if status.Code(err) != codes.Unavailable && status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("ordinary server remained callable after helper returned: %v", err)
	}
}

func TestIMTriggerWaitPropagatesStartPanicToCallerCleanup(t *testing.T) {
	const originalPanic = "ordinary startup failed"
	var recovered any
	cleaned := false
	func() {
		defer func() { recovered = recover() }()
		defer func() { cleaned = true }()
		waitIMTriggerServers(func() { panic(originalPanic) }, nil, nil)
	}()
	if recovered != originalPanic || !cleaned {
		t.Fatalf("startup panic lost caller cleanup: recovered=%v cleaned=%t", recovered, cleaned)
	}
	if stopped := waitIMTriggerServers(func() {}, nil, nil); stopped {
		t.Fatal("normal ordinary completion reported dedicated failure")
	}
}
