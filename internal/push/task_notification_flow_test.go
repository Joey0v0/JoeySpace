package push

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/internal/ws"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type notificationFlowTeams struct{ calls atomic.Int32 }

func (f *notificationFlowTeams) CheckTeamMember(context.Context, *userpb.CheckTeamMemberRequest, ...grpc.CallOption) (*userpb.CheckTeamMemberResponse, error) {
	f.calls.Add(1)
	return nil, errors.New("offline recipient must not call User")
}

// These temporary credentials allow the production client and handler to share
// a real TLS connection, without relying on production certificates or servers.
func notificationFlowCredentials(t *testing.T) (rpcauth.CertificateFiles, rpcauth.CertificateFiles) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Hour)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "notification-flow-test"},
		NotBefore: start, NotAfter: start.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	issue := func(name string, serial int64, usage x509.ExtKeyUsage) rpcauth.CertificateFiles {
		leafPub, leafKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{name},
			NotBefore: start, NotAfter: start.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
			ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, leafPub, key)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
		if err != nil {
			t.Fatal(err)
		}
		files := rpcauth.CertificateFiles{CertFile: filepath.Join(dir, name+".pem"), KeyFile: filepath.Join(dir, name+".key"), CAFile: filepath.Join(dir, "ca.pem")}
		for path, block := range map[string]*pem.Block{files.CertFile: {Type: "CERTIFICATE", Bytes: der}, files.KeyFile: {Type: "PRIVATE KEY", Bytes: keyDER}, files.CAFile: {Type: "CERTIFICATE", Bytes: caDER}} {
			if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return files
	}
	return issue("push.internal", 2, x509.ExtKeyUsageClientAuth), issue("ws.internal", 3, x509.ExtKeyUsageServerAuth)
}

func TestTaskNotificationFlowStaleOnlineRouteCommitsAfterRealTLSOfflineResponse(t *testing.T) {
	pushFiles, wsFiles := notificationFlowCredentials(t)
	tlsConfig, err := rpcauth.NewNotificationServerTLSConfig(wsFiles, "push.internal")
	if err != nil {
		t.Fatal(err)
	}
	teams := &notificationFlowTeams{}
	handler := ws.NewTaskNotificationHandler(ws.NewHub(zap.NewNop()), teams, "push.internal")
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler.ServeHTTP(w, r)
	}))
	server.TLS = tlsConfig
	server.StartTLS()
	defer server.Close()
	client, err := NewTaskNotificationClient(TaskNotificationClientConfig{Files: pushFiles, WSDNSName: "ws.internal", Routes: map[string]string{"im-ws:9091": server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	event, message := consumerNotificationMessage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fetches, commits := 0, 0
	var outcome string
	reader := &notificationReaderFake{fetch: func(context.Context) (kafka.Message, error) {
		fetches++
		return message, nil
	}, commit: func(_ context.Context, messages ...kafka.Message) error {
		commits++
		if outcome != model.TaskNotificationOffline || len(messages) != 1 || messages[0].Offset != message.Offset {
			t.Fatal("commit occurred without original event's authenticated offline response")
		}
		cancel()
		return nil
	}}
	online := notificationOnlineFake(func(context.Context, int64) (string, error) { return "im-ws:9091", nil })
	sender := notificationSenderFake(func(ctx context.Context, addr string, e model.TaskNotificationEvent) (model.TaskNotificationDelivery, error) {
		if e != event {
			t.Fatal("event changed between reader and sender")
		}
		result, err := client.Send(ctx, addr, e)
		outcome = result.Outcome
		return result, err
	})
	consumer := notificationTestConsumer(t, reader, online, sender)
	defer consumer.Close()
	if err := consumer.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if fetches != 1 || commits != 1 || requests.Load() != 1 || teams.calls.Load() != 0 {
		t.Fatalf("fetches/commits/requests/User calls: %d/%d/%d/%d", fetches, commits, requests.Load(), teams.calls.Load())
	}
}
