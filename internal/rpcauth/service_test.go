package rpcauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func TestRequireServiceIdentityUsesVerifiedExactSAN(t *testing.T) {
	leaf := &x509.Certificate{Raw: []byte{1}, DNSNames: []string{"im.go-im.internal"}}
	base := tls.ConnectionState{Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
	ctx := func(s tls.ConnectionState) context.Context {
		return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: s}})
	}
	if err := RequireServiceIdentity(ctx(base), "im.go-im.internal"); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*tls.ConnectionState){
		"no chain": func(s *tls.ConnectionState) { s.VerifiedChains = nil },
		"other chain": func(s *tls.ConnectionState) {
			s.VerifiedChains = [][]*x509.Certificate{{{Raw: []byte{2}, DNSNames: leaf.DNSNames}}}
		},
		"old TLS":        func(s *tls.ConnectionState) { s.Version = tls.VersionTLS12 },
		"no certificate": func(s *tls.ConnectionState) { s.PeerCertificates = nil },
		"wrong SAN": func(s *tls.ConnectionState) {
			c := &x509.Certificate{Raw: []byte{3}, DNSNames: []string{"agent.go-im.internal"}}
			s.PeerCertificates = []*x509.Certificate{c}
			s.VerifiedChains = [][]*x509.Certificate{{c}}
		},
		"CN only": func(s *tls.ConnectionState) {
			c := &x509.Certificate{Raw: []byte{4}}
			c.Subject.CommonName = "im.go-im.internal"
			s.PeerCertificates = []*x509.Certificate{c}
			s.VerifiedChains = [][]*x509.Certificate{{c}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := base
			mutate(&s)
			if status.Code(RequireServiceIdentity(ctx(s), "im.go-im.internal")) != codes.Unauthenticated {
				t.Fatal("accepted unverified service")
			}
		})
	}
	for _, c := range []context.Context{context.Background(), metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer forged", "service", "im.go-im.internal"))} {
		if status.Code(RequireServiceIdentity(c, "im.go-im.internal")) != codes.Unauthenticated {
			t.Fatal("metadata used as service proof")
		}
	}
	for _, name := range []string{"", "*.go-im.internal", " im.go-im.internal", "im.go-im.internal\n", "other.go-im.internal"} {
		if status.Code(RequireServiceIdentity(ctx(base), name)) != codes.Unauthenticated {
			t.Fatal("invalid identity accepted")
		}
	}
}

func TestServiceCredentialsValidateNamesAndUseMutualTLS13(t *testing.T) {
	for _, name := range []string{"", "*.internal", " im.internal", "im\t.internal"} {
		if _, e := NewServiceClientCredentials(CertificateFiles{}, name); e == nil {
			t.Fatal("bad client name")
		}
		if _, e := NewServiceServerCredentials(CertificateFiles{}, name); e == nil {
			t.Fatal("bad server name")
		}
	}
	ca := newTestCA(t)
	server := issueTestCertificate(t, ca, "user.go-im.internal", x509.ExtKeyUsageServerAuth, false)
	client := issueTestCertificate(t, ca, "im.go-im.internal", x509.ExtKeyUsageClientAuth, false)
	s, e := NewServiceServerCredentials(server, "im.go-im.internal")
	if e != nil {
		t.Fatal(e)
	}
	c, e := NewServiceClientCredentials(client, "user.go-im.internal")
	if e != nil {
		t.Fatal(e)
	}
	if s.Info().SecurityProtocol != "tls" || c.Info().SecurityProtocol != "tls" || c.Info().ServerName != "user.go-im.internal" {
		t.Fatalf("unexpected credentials: %+v %+v", s.Info(), c.Info())
	}
}

func TestServiceClientRejectsWildcardServerSANOnActualTLSConnection(t *testing.T) {
	ca := newTestCA(t)
	serverFiles := issueTestCertificate(t, ca, "*.go-im.internal", x509.ExtKeyUsageServerAuth, false)
	clientFiles := issueTestCertificate(t, ca, "im.go-im.internal", x509.ExtKeyUsageClientAuth, false)
	screds, e := NewServiceServerCredentials(serverFiles, "im.go-im.internal")
	if e != nil {
		t.Fatal(e)
	}
	cCreds, e := NewServiceClientCredentials(clientFiles, "user.go-im.internal")
	if e != nil {
		t.Fatal(e)
	}
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	s := grpc.NewServer(grpc.Creds(screds))
	impl := &countingHealthServer{}
	healthpb.RegisterHealthServer(s, impl)
	go s.Serve(l)
	defer s.Stop()
	conn, e := grpc.NewClient(l.Addr().String(), grpc.WithTransportCredentials(cCreds), grpc.WithDisableRetry())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err == nil || impl.calls.Load() != 0 {
		t.Fatal("wildcard server was accepted as exact User identity")
	}
}
