package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestBotReplyClientDisabledOrFailsClosed(t *testing.T) {
	client, closeClient, err := NewBotReplyClient(func(string) string { return "" })
	if err != nil || client != nil || closeClient == nil {
		t.Fatalf("disabled = %v, %v", client, err)
	}
	closeClient()
	for _, tc := range []struct {
		name, addr, server string
		partial            bool
	}{
		{"partial", "im-rpc:9005", "", true},
		{"bad address", "http://im-rpc:9005", "im.go-im.internal", false},
		{"bad port", "im-rpc:0", "im.go-im.internal", false},
		{"wildcard identity", "im-rpc:9005", "*.internal", false},
		{"missing certificates", "im-rpc:9005", "im.go-im.internal", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{"AGENT_IM_BOT_ADDR": tc.addr, "AGENT_IM_BOT_SERVER_NAME": tc.server}
			if !tc.partial {
				for _, n := range []string{"CERT", "KEY", "CA"} {
					values["AGENT_IM_BOT_TLS_"+n+"_FILE"] = "private-nonexistent-file"
				}
			}
			client, closeClient, err := NewBotReplyClient(func(n string) string { return values[n] })
			if client != nil || closeClient != nil || err == nil || strings.Contains(err.Error(), "private-nonexistent-file") {
				t.Fatalf("invalid = %v, %v", client, err)
			}
		})
	}
}

func botClientCertificates(t *testing.T) map[string]rpcauth.CertificateFiles {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	files := map[string]rpcauth.CertificateFiles{}
	for i, name := range []string{"im.go-im.internal", "agent.go-im.internal", "other.go-im.internal"} {
		leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(int64(i + 2)), DNSNames: []string{name},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
		if err != nil {
			t.Fatal(err)
		}
		f := rpcauth.CertificateFiles{CertFile: filepath.Join(dir, name+".pem"), KeyFile: filepath.Join(dir, name+".key"), CAFile: caPath}
		if err := os.WriteFile(f.CertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
			t.Fatal(err)
		}
		files[name] = f
	}
	return files
}

type tlsBotTestServer struct {
	impb.UnimplementedIMBotServer
	calls atomic.Int32
	t     *testing.T
}

func (s *tlsBotTestServer) PostTaskCreatedCard(ctx context.Context, req *impb.PostTaskCreatedCardRequest) (*impb.PostTaskCreatedCardResponse, error) {
	s.calls.Add(1)
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" || req.GetRunId() != 9001 || req.GetTeamId() != 200 || req.GetGroupId() != 300 || req.GetContent() == "" {
		s.t.Error("TLS client lost user credential or run")
	}
	return &impb.PostTaskCreatedCardResponse{MsgId: "bot-task:9001", Accepted: true}, nil
}

func TestProductionBotClientUsesMutualTLSAndVerifiesIMName(t *testing.T) {
	files := botClientCertificates(t)
	creds, err := rpcauth.NewIMServerCredentials(files["im.go-im.internal"], "agent.go-im.internal")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	service := &tlsBotTestServer{t: t}
	server := grpc.NewServer(grpc.Creds(creds))
	impb.RegisterIMBotServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	for _, tc := range []struct {
		name, identity, serverName string
		want                       codes.Code
	}{
		{"trusted", "agent.go-im.internal", "im.go-im.internal", codes.OK},
		{"wrong IM name", "agent.go-im.internal", "wrong.go-im.internal", codes.Unavailable},
		{"other trusted service", "other.go-im.internal", "im.go-im.internal", codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := files[tc.identity]
			values := map[string]string{"AGENT_IM_BOT_ADDR": listener.Addr().String(), "AGENT_IM_BOT_SERVER_NAME": tc.serverName,
				"AGENT_IM_BOT_TLS_CERT_FILE": f.CertFile, "AGENT_IM_BOT_TLS_KEY_FILE": f.KeyFile, "AGENT_IM_BOT_TLS_CA_FILE": f.CAFile}
			client, closeClient, err := NewBotReplyClient(func(n string) string { return values[n] })
			if err != nil {
				t.Fatal(err)
			}
			defer closeClient()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer user-token"))
			if tc.want == codes.OK {
				run := succeededReplyRun()
				impl := NewServer(nil)
				impl.draftReader = confirmationReader(t, func(context.Context, int64, int64) (taskDraftRun, error) { return run, nil }, nil)
				impl.replier = &draftReplier{store: &memoryReplyStore{}, bot: client}
				resp, err := replyRPCClient(t, impl).RetryTaskReply(ctx, &pb.GetTaskDraftRequest{RunId: run.ID})
				if err != nil || resp.GetReplyStatus() != "accepted" || resp.GetReplyMsgId() != "bot-task:9001" {
					t.Fatalf("Agent to TLS IM = %v, %v", resp, err)
				}
				return
			}
			resp, err := client.PostTaskCreatedCard(ctx, &impb.PostTaskCreatedCardRequest{RunId: 9001})
			if status.Code(err) != tc.want || (err == nil && !resp.GetAccepted()) {
				t.Fatalf("TLS = %v, %v", resp, err)
			}
		})
	}
	if service.calls.Load() != 1 {
		t.Fatalf("rejected peers reached handler: %d", service.calls.Load())
	}
}
