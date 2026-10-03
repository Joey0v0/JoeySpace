package rpcauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	path string
}

func newTestCA(t *testing.T) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return testCA{cert: cert, key: key, path: path}
}

func issueTestCertificate(t *testing.T, ca testCA, dnsName string, purpose x509.ExtKeyUsage, expired bool) CertificateFiles {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(time.Hour)
	if expired {
		expiry = time.Now().Add(-time.Minute)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "agent.go-im.internal"},
		DNSNames: []string{dnsName}, NotBefore: time.Now().Add(-time.Hour), NotAfter: expiry,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{purpose},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := CertificateFiles{CertFile: filepath.Join(dir, "cert.pem"), KeyFile: filepath.Join(dir, "key.pem"), CAFile: ca.path}
	for path, data := range map[string][]byte{
		files.CertFile: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		files.KeyFile:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

type countingHealthServer struct {
	healthpb.UnimplementedHealthServer
	calls atomic.Int64
}

func (s *countingHealthServer) Check(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	s.calls.Add(1)
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func TestMutualTLSAllowsOnlyTrustedAgentOnRealGRPCConnection(t *testing.T) {
	ca, otherCA := newTestCA(t), newTestCA(t)
	serverFiles := issueTestCertificate(t, ca, "im.go-im.internal", x509.ExtKeyUsageServerAuth, false)
	agentFiles := issueTestCertificate(t, ca, "agent.go-im.internal", x509.ExtKeyUsageClientAuth, false)
	serverCreds, err := NewIMServerCredentials(serverFiles, "agent.go-im.internal")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(serverCreds))
	handler := &countingHealthServer{}
	healthpb.RegisterHealthServer(server, handler)
	go server.Serve(listener)
	t.Cleanup(server.Stop)

	untrustedAgent := issueTestCertificate(t, otherCA, "agent.go-im.internal", x509.ExtKeyUsageClientAuth, false)
	untrustedAgent.CAFile = ca.path // Trust IM, but IM must reject this client issuer.
	wrongServerCA := agentFiles
	wrongServerCA.CAFile = otherCA.path
	noClientCertRoots := x509.NewCertPool()
	caPEM, err := os.ReadFile(ca.path)
	if err != nil || !noClientCertRoots.AppendCertsFromPEM(caPEM) {
		t.Fatalf("load test CA: %v", err)
	}
	for _, tc := range []struct {
		name       string
		files      CertificateFiles
		serverName string
		creds      credentials.TransportCredentials
		allowed    bool
	}{
		{name: "trusted Agent", files: agentFiles, serverName: "im.go-im.internal", allowed: true},
		{name: "other service even with Agent common name", files: issueTestCertificate(t, ca, "task.go-im.internal", x509.ExtKeyUsageClientAuth, false), serverName: "im.go-im.internal"},
		{name: "wildcard is not exact Agent identity", files: issueTestCertificate(t, ca, "*.go-im.internal", x509.ExtKeyUsageClientAuth, false), serverName: "im.go-im.internal"},
		{name: "untrusted client issuer", files: untrustedAgent, serverName: "im.go-im.internal"},
		{name: "untrusted server issuer", files: wrongServerCA, serverName: "im.go-im.internal"},
		{name: "wrong server name", files: agentFiles, serverName: "other.go-im.internal"},
		{name: "expired Agent", files: issueTestCertificate(t, ca, "agent.go-im.internal", x509.ExtKeyUsageClientAuth, true), serverName: "im.go-im.internal"},
		{name: "wrong client certificate purpose", files: issueTestCertificate(t, ca, "agent.go-im.internal", x509.ExtKeyUsageServerAuth, false), serverName: "im.go-im.internal"},
		{name: "missing client certificate", creds: credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: noClientCertRoots, ServerName: "im.go-im.internal"})},
		{name: "plaintext client", creds: insecure.NewCredentials()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientCreds := tc.creds
			if clientCreds == nil {
				clientCreds, err = NewAgentClientCredentials(tc.files, tc.serverName)
				if err != nil {
					t.Fatal(err)
				}
			}
			conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(clientCreds))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			before := handler.calls.Load()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
			if tc.allowed {
				if err != nil || resp.GetStatus() != healthpb.HealthCheckResponse_SERVING || handler.calls.Load() != before+1 {
					t.Fatalf("trusted Agent failed: resp=%v err=%v", resp, err)
				}
			} else if err == nil || handler.calls.Load() != before {
				t.Fatalf("unauthorized connection reached handler: resp=%v err=%v", resp, err)
			}
		})
	}
}

func TestMutualTLSCredentialsRejectIncompleteOrInvalidConfiguration(t *testing.T) {
	ca := newTestCA(t)
	files := issueTestCertificate(t, ca, "agent.go-im.internal", x509.ExtKeyUsageClientAuth, false)
	invalidCA := filepath.Join(t.TempDir(), "invalid-ca.pem")
	if err := os.WriteFile(invalidCA, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	other := issueTestCertificate(t, ca, "other.go-im.internal", x509.ExtKeyUsageClientAuth, false)
	for _, bad := range []CertificateFiles{
		{}, {KeyFile: files.KeyFile, CAFile: files.CAFile},
		{CertFile: files.CertFile, CAFile: files.CAFile},
		{CertFile: files.CertFile, KeyFile: files.KeyFile},
		{CertFile: files.CertFile, KeyFile: files.KeyFile, CAFile: invalidCA},
		{CertFile: files.CertFile, KeyFile: other.KeyFile, CAFile: files.CAFile},
		{CertFile: files.CertFile + ".missing", KeyFile: files.KeyFile, CAFile: files.CAFile},
	} {
		if creds, err := NewAgentClientCredentials(bad, "im.go-im.internal"); err == nil || creds != nil {
			t.Fatalf("client accepted bad config: %+v", bad)
		}
		if creds, err := NewIMServerCredentials(bad, "agent.go-im.internal"); err == nil || creds != nil {
			t.Fatalf("server accepted bad config: %+v", bad)
		}
	}
	if creds, err := NewAgentClientCredentials(files, ""); err == nil || creds != nil {
		t.Fatal("client accepted empty server identity")
	}
	for _, name := range []string{"", "  ", "*.go-im.internal"} {
		if creds, err := NewIMServerCredentials(files, name); err == nil || creds != nil {
			t.Fatalf("server accepted ambiguous Agent identity: %q", name)
		}
	}
}
