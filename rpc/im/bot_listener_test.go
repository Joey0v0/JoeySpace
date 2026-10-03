package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"
)

func botTestCertificates(t *testing.T) (rpcauth.CertificateFiles, rpcauth.CertificateFiles, rpcauth.CertificateFiles) {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "local IM test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	issue := func(name string, role x509.ExtKeyUsage, serial int64) rpcauth.CertificateFiles {
		childKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		child := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{name},
			NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{role}}
		der, err := x509.CreateCertificate(rand.Reader, child, ca, &childKey.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		private, err := x509.MarshalPKCS8PrivateKey(childKey)
		if err != nil {
			t.Fatal(err)
		}
		files := rpcauth.CertificateFiles{CertFile: filepath.Join(dir, name+".pem"), KeyFile: filepath.Join(dir, name+".key"), CAFile: caPath}
		if err := os.WriteFile(files.CertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(files.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
			t.Fatal(err)
		}
		return files
	}
	return issue("im.go-im.internal", x509.ExtKeyUsageServerAuth, 2), issue("agent.go-im.internal", x509.ExtKeyUsageClientAuth, 3), issue("task.go-im.internal", x509.ExtKeyUsageClientAuth, 4)
}

func TestBotConfigIsOptInAndPartialConfigurationFails(t *testing.T) {
	values := map[string]string{"IM_BOT_LISTEN_ON": "127.0.0.1:9005", "IM_BOT_CODE": "task-assistant",
		"IM_BOT_AGENT_DNS_NAME": "agent.go-im.internal", "IM_BOT_KAFKA_BROKERS": "localhost:19092", "IM_BOT_KAFKA_TOPIC": "chat_messages",
		"IM_BOT_TLS_CERT_FILE": "cert.pem", "IM_BOT_TLS_KEY_FILE": "key.pem", "IM_BOT_TLS_CA_FILE": "ca.pem"}
	if c, err := loadBotReplyConfig(func(string) string { return "" }); err != nil || c.ListenOn != "" {
		t.Fatal(c, err)
	}
	if _, err := loadBotReplyConfig(func(k string) string { return values[k] }); err != nil {
		t.Fatal(err)
	}
	for key := range values {
		if _, err := loadBotReplyConfig(func(k string) string {
			if k == key {
				return ""
			}
			return values[k]
		}); err == nil {
			t.Fatalf("accepted incomplete configuration missing %s", key)
		}
	}
	for _, bad := range []struct{ key, value string }{
		{"IM_BOT_LISTEN_ON", "localhost:abc"}, {"IM_BOT_KAFKA_BROKERS", "localhost:0"},
		{"IM_BOT_KAFKA_BROKERS", "localhost:19092,"}, {"IM_BOT_AGENT_DNS_NAME", "*.go-im.internal"},
		{"IM_BOT_AGENT_DNS_NAME", " agent.go-im.internal"}, {"IM_BOT_CODE", " task-assistant"},
	} {
		if _, err := loadBotReplyConfig(func(k string) string {
			if k == bad.key {
				return bad.value
			}
			return values[k]
		}); err == nil {
			t.Fatalf("accepted bad configuration: %+v", bad)
		}
	}
	im, _ := testIMServer(t)
	im.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
	c, _ := loadBotReplyConfig(func(k string) string { return values[k] })
	if runtime, err := newBotReplyRuntime(c, im); err == nil || runtime != nil {
		t.Fatal("invalid certificates enabled a listener")
	}
}

func TestBotComposeOverlayDoesNotPublishPortsOrEnableBaseDeployment(t *testing.T) {
	type service struct {
		Environment map[string]string `yaml:"environment"`
		Ports       []string          `yaml:"ports"`
		Volumes     []struct {
			Type     string `yaml:"type"`
			Target   string `yaml:"target"`
			ReadOnly bool   `yaml:"read_only"`
			Bind     struct {
				CreateHostPath bool `yaml:"create_host_path"`
			} `yaml:"bind"`
		} `yaml:"volumes"`
		DependsOn map[string]struct {
			Condition string `yaml:"condition"`
		} `yaml:"depends_on"`
	}
	var overlay struct {
		Services map[string]service `yaml:"services"`
	}
	data, err := os.ReadFile("../../deploy/docker-compose.bot.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &overlay); err != nil {
		t.Fatal(err)
	}
	im := overlay.Services["im-rpc"]
	if len(overlay.Services) != 2 || len(im.Ports) != 0 || im.Environment["IM_BOT_LISTEN_ON"] != "0.0.0.0:9005" ||
		im.Environment["IM_BOT_KAFKA_BROKERS"] != "kafka:19092" || len(im.Volumes) != 1 || !im.Volumes[0].ReadOnly || im.Volumes[0].Bind.CreateHostPath || im.DependsOn["kafka"].Condition != "service_healthy" {
		t.Fatalf("invalid bot overlay: %+v", im)
	}
	agent := overlay.Services["agent-rpc"]
	if len(agent.Ports) != 0 || agent.Environment["AGENT_IM_BOT_ADDR"] != "im-rpc:9005" ||
		agent.Environment["AGENT_IM_BOT_TLS_KEY_FILE"] != "/app/certs/agent/key.pem" ||
		len(agent.Volumes) != 1 || !agent.Volumes[0].ReadOnly || agent.Volumes[0].Bind.CreateHostPath ||
		agent.Volumes[0].Target != "/app/certs/agent" {
		t.Fatalf("invalid Agent bot overlay: %+v", agent)
	}
	var base struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	data, err = os.ReadFile("../../deploy/docker-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &base); err != nil {
		t.Fatal(err)
	}
	if base.Services["im-rpc"].Environment["IM_BOT_LISTEN_ON"] != "" {
		t.Fatal("base deployment enabled bot listener")
	}
}

func TestProductionBotListenerEnforcesTLSAndKeepsServicesSeparate(t *testing.T) {
	serverFiles, agentFiles, otherFiles := botTestCertificates(t)
	im, mock := testIMServer(t)
	im.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
	runtime, err := newBotReplyRuntime(botReplyConfig{ListenOn: "127.0.0.1:0", BotCode: "task-assistant", AgentDNSName: "agent.go-im.internal", Files: serverFiles, Brokers: []string{"localhost:19092"}, Topic: "chat_messages"}, im)
	if err != nil {
		t.Fatal(err)
	}
	go runtime.server.Serve(runtime.listener)
	t.Cleanup(runtime.Stop)
	if _, ok := runtime.server.GetServiceInfo()["im.IM"]; ok {
		t.Fatal("ordinary service registered on bot listener")
	}
	if _, ok := runtime.server.GetServiceInfo()["im.IMBot"]; !ok {
		t.Fatal("bot service missing")
	}
	intent := validBotIntent(t)
	req := &pb.PostTaskCreatedCardRequest{RunId: 9, TeamId: 200, GroupId: 300, Content: intent.Content}
	for _, tc := range []struct {
		name  string
		files rpcauth.CertificateFiles
		plain bool
		token bool
		want  codes.Code
	}{
		{"trusted Agent without user token", agentFiles, false, false, codes.Unauthenticated},
		{"trusted Agent retry of accepted result", agentFiles, false, true, codes.OK},
		{"other trusted service", otherFiles, false, true, codes.Unavailable},
		{"plaintext with valid user token", agentFiles, true, true, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.want == codes.OK {
				expectBotAccess(mock)
				expectBotProfile(mock, true)
				stored := storedBotRecord(intent)
				stored.Accepted = true
				mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `im_bot_sends`")).WillReturnError(&driver.MySQLError{Number: 1062})
				mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `im_bot_sends` WHERE msg_id = ? LIMIT ?")).WithArgs("bot-task:9", 1).WillReturnRows(botSendRows(stored))
			}
			creds := insecure.NewCredentials()
			if !tc.plain {
				creds, err = rpcauth.NewAgentClientCredentials(tc.files, "im.go-im.internal")
				if err != nil {
					t.Fatal(err)
				}
			}
			conn, err := grpc.NewClient(runtime.listener.Addr().String(), grpc.WithTransportCredentials(creds))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if tc.token {
				ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
			}
			response, err := pb.NewIMBotClient(conn).PostTaskCreatedCard(ctx, req)
			if status.Code(err) != tc.want {
				t.Fatalf("response=%v err=%v", response, err)
			}
			if tc.want == codes.OK && (!response.GetAccepted() || response.GetMsgId() != "bot-task:9") {
				t.Fatal(response)
			}
		})
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ordinary := grpc.NewServer()
	registerOrdinaryIM(ordinary, im)
	go ordinary.Serve(listener)
	t.Cleanup(ordinary.Stop)
	if _, ok := ordinary.GetServiceInfo()["im.IMBot"]; ok {
		t.Fatal("bot service leaked to plaintext port")
	}
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := pb.NewIMBotClient(conn).PostTaskCreatedCard(ctx, req); status.Code(err) != codes.Unimplemented {
		t.Fatalf("plaintext bot RPC: %v", err)
	}
}
