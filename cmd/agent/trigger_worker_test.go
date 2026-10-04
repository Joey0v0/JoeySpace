package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/yjydist/go-im/rpc/agent"
	agentpb "github.com/yjydist/go-im/rpc/agent/pb"
	"github.com/zeromicro/go-zero/zrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"
)

type workerRuntimeTestProcessor func(context.Context, agent.TriggerLease) error

func (p workerRuntimeTestProcessor) Process(ctx context.Context, lease agent.TriggerLease) error {
	return p(ctx, lease)
}

type workerRuntimeTestRunner func(context.Context) error

func (r workerRuntimeTestRunner) Run(ctx context.Context) error { return r(ctx) }
func waitWorkerRuntime(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runtime did not finish")
	}
}

func TestAgentTriggerWorkerDisabledDoesNotReadOrConstructResources(t *testing.T) {
	for _, value := range []string{"", "false"} {
		cfg, err := loadAgentTriggerWorkerConfig(func(key string) string {
			if key != "AGENT_TRIGGER_WORKER_ENABLED" {
				t.Fatalf("disabled read %s", key)
			}
			return value
		})
		if err != nil || cfg.Enabled {
			t.Fatalf("disabled config: %v %v", cfg, err)
		}
		source, err := prepareAgentTriggerWorkerSource(cfg, func(string) string { t.Fatal("disabled source environment read"); return "" }, func(func(string) string) (*agent.TriggerContextClient, error) {
			t.Fatal("disabled source factory")
			return nil, nil
		})
		if err != nil || source != nil {
			t.Fatal("disabled source prepared")
		}
		runtime, err := newAgentTriggerWorker(cfg, nil, nil, nil, func(*agent.Server, *agent.TriggerContextClient, *agent.TriggerInboxStore) (agent.TriggerProcessor, error) {
			t.Fatal("disabled processor constructed")
			return nil, nil
		})
		if err != nil || runtime != nil || runtime.Start(context.Background(), nil) != nil || runtime.Stop() != nil || runtime.stoppedError() != nil {
			t.Fatalf("disabled runtime: %v %v", runtime, err)
		}
	}
	if _, err := loadAgentTriggerWorkerConfig(nil); err == nil {
		t.Fatal("nil environment accepted")
	}
}

func TestAgentTriggerWorkerConfigurationPrecedesDBAndModels(t *testing.T) {
	for _, value := range []string{"TRUE", "1", " true", "false ", "\t"} {
		if _, err := loadAgentTriggerWorkerConfig(func(string) string { return value }); err == nil {
			t.Fatalf("invalid switch %q", value)
		}
	}
	cfg, err := loadAgentTriggerWorkerConfig(func(string) string { return "true" })
	if err != nil || !cfg.Enabled {
		t.Fatal("enabled rejected")
	}
	for _, values := range []map[string]string{{}, {"AGENT_IM_TRIGGER_ADDR": "im:9098"}, {"AGENT_IM_TRIGGER_ADDR": "dns:///im:9098", "AGENT_IM_TRIGGER_SERVER_NAME": "im.internal", "AGENT_IM_TRIGGER_TLS_CERT_FILE": "private.cert", "AGENT_IM_TRIGGER_TLS_KEY_FILE": "private.key", "AGENT_IM_TRIGGER_TLS_CA_FILE": "private.ca"}} {
		if source, err := prepareAgentTriggerWorkerSource(cfg, func(key string) string { return values[key] }, nil); err == nil || source != nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("partial/invalid TLS config accepted/leaked: %v", err)
		}
	}
	t.Setenv("AGENT_TRIGGER_INBOX_ENABLED", "false")
	t.Setenv("AGENT_MYSQL_DSN", "private-invalid-dsn")
	t.Setenv("AGENT_TRIGGER_WORKER_ENABLED", "invalid")
	if err := runAgent(zrpc.RpcServerConf{}); err == nil || !strings.Contains(err.Error(), "worker switch") {
		t.Fatalf("worker config did not precede DB: %v", err)
	}
	t.Setenv("AGENT_TRIGGER_WORKER_ENABLED", "true")
	for _, key := range []string{"AGENT_IM_TRIGGER_ADDR", "AGENT_IM_TRIGGER_SERVER_NAME", "AGENT_IM_TRIGGER_TLS_CERT_FILE", "AGENT_IM_TRIGGER_TLS_KEY_FILE", "AGENT_IM_TRIGGER_TLS_CA_FILE"} {
		t.Setenv(key, "")
	}
	if err := runAgent(zrpc.RpcServerConf{}); err == nil || !strings.Contains(err.Error(), "mTLS") || strings.Contains(err.Error(), "private") {
		t.Fatalf("TLS configuration did not precede DB: %v", err)
	}
}

func TestAgentTriggerWorkerReusesExactConfiguredServerAndDedicatedSource(t *testing.T) {
	server := agent.NewServer(nil)
	source := &agent.TriggerContextClient{}
	db := &gorm.DB{}
	var calls int
	runtime, err := newAgentTriggerWorker(agentTriggerWorkerConfig{Enabled: true}, server, source, db, func(s *agent.Server, c *agent.TriggerContextClient, store *agent.TriggerInboxStore) (agent.TriggerProcessor, error) {
		calls++
		if s != server || c != source || store == nil {
			t.Fatal("processor lost existing server/source/store")
		}
		return workerRuntimeTestProcessor(func(context.Context, agent.TriggerLease) error {
			t.Fatal("construction ran model/processor")
			return nil
		}), nil
	})
	if err != nil || runtime == nil || calls != 1 {
		t.Fatalf("construction: %v", err)
	}
	if err := runtime.Stop(); err != nil {
		t.Fatal(err)
	}
	if runtime.Start(context.Background(), nil) == nil {
		t.Fatal("closed prepared runtime restarted")
	}
	for _, tc := range []struct {
		s  *agent.Server
		c  *agent.TriggerContextClient
		db *gorm.DB
	}{{nil, source, db}, {server, nil, db}, {server, source, nil}} {
		if r, err := newAgentTriggerWorker(agentTriggerWorkerConfig{Enabled: true}, tc.s, tc.c, tc.db, nil); err == nil || r != nil {
			t.Fatal("missing production dependency accepted")
		}
	}
	for _, failure := range []string{"error", "nil", "typed_nil"} {
		if r, err := newAgentTriggerWorker(agentTriggerWorkerConfig{Enabled: true}, server, source, db, func(*agent.Server, *agent.TriggerContextClient, *agent.TriggerInboxStore) (agent.TriggerProcessor, error) {
			switch failure {
			case "error":
				return nil, errors.New("private credentials")
			case "typed_nil":
				var p workerRuntimeTestProcessor
				return p, nil
			default:
				return nil, nil
			}
		}); err == nil || r != nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe processor construction: %v", err)
		}
	}
}

func TestAgentTriggerWorkerStopsAfterRunBeforeClosingClientExactlyOnce(t *testing.T) {
	started := make(chan struct{})
	allowFinish := make(chan struct{})
	canceled := make(chan struct{})
	var finished atomic.Bool
	var closes, reports atomic.Int32
	runtime := &agentTriggerWorkerRuntime{worker: workerRuntimeTestRunner(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-allowFinish
		finished.Store(true)
		return ctx.Err()
	}), closeClient: func() error {
		if !finished.Load() {
			t.Error("source closed before Run exited")
		}
		closes.Add(1)
		return nil
	}}
	if err := runtime.Start(context.Background(), func(error) { reports.Add(1) }); err != nil {
		t.Fatal(err)
	}
	<-started
	stopDone := make(chan struct{})
	go func() {
		if err := runtime.Stop(); err != nil {
			t.Error(err)
		}
		close(stopDone)
	}()
	<-canceled
	if closes.Load() != 0 {
		t.Fatal("did not wait canceled processor")
	}
	close(allowFinish)
	waitWorkerRuntime(t, stopDone)
	var group sync.WaitGroup
	for i := 0; i < 5; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := runtime.Stop(); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if closes.Load() != 1 || reports.Load() != 0 || runtime.stoppedError() != nil || runtime.Start(context.Background(), nil) == nil {
		t.Fatal("stop idempotency/cancellation isolation failed")
	}
}

func TestAgentTriggerWorkerShutdownLimitAndPreparedCloseAreSafe(t *testing.T) {
	finish := make(chan struct{})
	canceled := make(chan struct{})
	var closes atomic.Int32
	runtime := &agentTriggerWorkerRuntime{stopWait: 20 * time.Millisecond, worker: workerRuntimeTestRunner(func(ctx context.Context) error { <-ctx.Done(); close(canceled); <-finish; return ctx.Err() }), closeClient: func() error { closes.Add(1); return errors.New("private TLS key") }}
	if err := runtime.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Stop(); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("unbounded/unsafe shutdown: %v", err)
	}
	<-canceled
	if closes.Load() != 1 || runtime.Start(context.Background(), nil) == nil {
		t.Fatal("timed out worker was restarted")
	}
	close(finish)
	waitWorkerRuntime(t, runtime.done)
	if runtime.Stop() == nil || closes.Load() != 1 {
		t.Fatal("timeout/close result changed")
	}
	prepared := &agentTriggerWorkerRuntime{closeClient: func() error { closes.Add(1); return errors.New("private connection") }}
	if err := prepared.Stop(); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("prepared close error leaked")
	}
	_ = prepared.Stop()
	if closes.Load() != 2 {
		t.Fatal("prepared close repeated")
	}
}

func TestAgentTriggerWorkerFailureDoesNotStopOrdinaryRPCOrInbox(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	answerer := &inboxRuntimeAnswerer{t: t}
	agentpb.RegisterAgentServer(server, agent.NewServer(answerer))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := agentpb.NewAgentClient(conn)
	ask := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer person-token"))
		result, err := client.Ask(ctx, &agentpb.AskRequest{TeamId: 200, GroupId: 300, Question: "本人问答"})
		if err != nil || result.GetAnswer() != "本人问答仍可用" {
			t.Fatalf("ordinary RPC changed: %v %v", result, err)
		}
	}
	var inboxStopped atomic.Bool
	inbox := &agentTriggerInbox{consumer: &inboxRuntimeTestConsumer{run: func(ctx context.Context) error { <-ctx.Done(); inboxStopped.Store(true); return ctx.Err() }, close: func() error { return nil }}}
	if err := inbox.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	defer inbox.Stop()
	ask()
	var runs, reports, closes atomic.Int32
	runtime := &agentTriggerWorkerRuntime{worker: workerRuntimeTestRunner(func(context.Context) error { runs.Add(1); return errors.New("private database credentials") }), closeClient: func() error { closes.Add(1); return nil }}
	if err := runtime.Start(context.Background(), func(err error) {
		reports.Add(1)
		if strings.Contains(err.Error(), "private") {
			t.Error("worker fault leaked")
		}
	}); err != nil {
		t.Fatal(err)
	}
	waitWorkerRuntime(t, runtime.done)
	ask()
	if runtime.stoppedError() == nil || runs.Load() != 1 || reports.Load() != 1 || closes.Load() != 0 || inboxStopped.Load() || answerer.calls.Load() != 2 {
		t.Fatal("worker failure crossed service/intake boundary")
	}
	if runtime.Start(context.Background(), nil) == nil {
		t.Fatal("failed worker auto/manual restarted")
	}
	if err := runtime.Stop(); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 || inboxStopped.Load() {
		t.Fatal("source close stopped independent intake")
	}
}

func TestAgentTriggerWorkerRuntimeRejectsInvalidContextAndReportsUnexpectedSuccess(t *testing.T) {
	var nilRunner workerRuntimeTestRunner
	for _, runner := range []agentTriggerWorkerRunner{nil, nilRunner} {
		if (&agentTriggerWorkerRuntime{worker: runner}).Start(context.Background(), nil) == nil {
			t.Fatal("nil runner accepted")
		}
	}
	runtime := &agentTriggerWorkerRuntime{worker: workerRuntimeTestRunner(func(context.Context) error { return nil })}
	if runtime.Start(nil, nil) == nil {
		t.Fatal("nil caller context accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(runtime.Start(ctx, nil), context.Canceled) || runtime.started {
		t.Fatal("canceled startup consumed runtime")
	}
	var reports atomic.Int32
	if err := runtime.Start(context.Background(), func(error) { reports.Add(1) }); err != nil {
		t.Fatal(err)
	}
	waitWorkerRuntime(t, runtime.done)
	if reports.Load() != 1 || runtime.stoppedError() == nil {
		t.Fatal("unexpected idle exit reported as healthy running worker")
	}
	if err := runtime.Stop(); err != nil {
		t.Fatal(err)
	}
}

var _ agent.TriggerProcessor = workerRuntimeTestProcessor(nil)
var _ agentTriggerWorkerRunner = workerRuntimeTestRunner(nil)
