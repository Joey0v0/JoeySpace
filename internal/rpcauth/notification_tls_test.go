package rpcauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func serveNotificationTLSForTest(t *testing.T, config *tls.Config, pushName string) (string, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	calls := new(atomic.Int64)
	server := &http.Server{ReadHeaderTimeout: time.Second, ErrorLog: log.New(io.Discard, "", 0),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.TLS == nil || !r.TLS.HandshakeComplete || r.TLS.Version != tls.VersionTLS13 || VerifyNotificationPeer(r.TLS, pushName) != nil {
				t.Error("HTTP handler received an unverified or incomplete TLS connection")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(tls.NewListener(listener, config)) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-finished; err != nil && err != http.ErrServerClosed {
			t.Errorf("TLS server shutdown: %v", err)
		}
	})
	return "https://" + listener.Addr().String() + "/internal/task-notifications", calls
}

func TestNotificationMutualTLSOnlyAllowsExactPushOnRealHTTPConnection(t *testing.T) {
	const wsName, pushName = "ws.go-im.internal", "push.go-im.internal"
	ca, otherCA := newTestCA(t), newTestCA(t)
	serverFiles := issueTestCertificate(t, ca, wsName, x509.ExtKeyUsageServerAuth, false)
	pushFiles := issueTestCertificate(t, ca, pushName, x509.ExtKeyUsageClientAuth, false)
	serverConfig, err := NewNotificationServerTLSConfig(serverFiles, pushName)
	if err != nil {
		t.Fatal(err)
	}
	if serverConfig.MinVersion != tls.VersionTLS13 || serverConfig.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatal("notification server must require mutually verified TLS 1.3")
	}
	url, calls := serveNotificationTLSForTest(t, serverConfig, pushName)
	untrustedPush := issueTestCertificate(t, otherCA, pushName, x509.ExtKeyUsageClientAuth, false)
	untrustedPush.CAFile = ca.path // The client trusts WS, but WS must reject the client issuer.
	wrongServerCA := pushFiles
	wrongServerCA.CAFile = otherCA.path
	for _, tc := range []struct {
		name    string
		files   CertificateFiles
		wsName  string
		mutate  func(*tls.Config)
		plain   bool
		allowed bool
	}{
		{name: "trusted Push", files: pushFiles, wsName: wsName, allowed: true},
		{name: "same CA wrong service", files: issueTestCertificate(t, ca, "task.go-im.internal", x509.ExtKeyUsageClientAuth, false), wsName: wsName},
		{name: "wildcard Push SAN", files: issueTestCertificate(t, ca, "*.go-im.internal", x509.ExtKeyUsageClientAuth, false), wsName: wsName},
		{name: "wrong client CA", files: untrustedPush, wsName: wsName},
		{name: "wrong WS CA", files: wrongServerCA, wsName: wsName},
		{name: "expired Push", files: issueTestCertificate(t, ca, pushName, x509.ExtKeyUsageClientAuth, true), wsName: wsName},
		{name: "wrong certificate purpose", files: issueTestCertificate(t, ca, pushName, x509.ExtKeyUsageServerAuth, false), wsName: wsName},
		{name: "wrong WS name", files: pushFiles, wsName: "other.go-im.internal"},
		{name: "no client certificate", files: pushFiles, wsName: wsName, mutate: func(c *tls.Config) { c.Certificates = nil }},
		{name: "TLS 1.2 only", files: pushFiles, wsName: wsName, mutate: func(c *tls.Config) { c.MinVersion, c.MaxVersion = tls.VersionTLS12, tls.VersionTLS12 }},
		{name: "plaintext", plain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestURL := url
			transport := &http.Transport{Proxy: nil}
			if tc.plain {
				requestURL = strings.Replace(url, "https://", "http://", 1)
			} else {
				config, err := NewNotificationClientTLSConfig(tc.files, tc.wsName)
				if err != nil {
					t.Fatal(err)
				}
				if config.MinVersion != tls.VersionTLS13 || config.InsecureSkipVerify || config.ServerName != tc.wsName {
					t.Fatal("client must verify TLS 1.3 and configured WS identity")
				}
				if tc.mutate != nil {
					tc.mutate(config)
				}
				transport.TLSClientConfig = config
			}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: time.Second}
			request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, requestURL, strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			before := calls.Load()
			response, err := client.Do(request)
			if response != nil {
				defer response.Body.Close()
			}
			if tc.allowed {
				if err != nil || response == nil || response.StatusCode != http.StatusNoContent || calls.Load() != before+1 {
					t.Fatalf("trusted Push failed: response=%v error=%v calls=%d", response, err, calls.Load()-before)
				}
			} else if calls.Load() != before || err == nil && response != nil && response.StatusCode == http.StatusNoContent {
				t.Fatalf("unauthorized request entered handler: response=%v error=%v calls=%d", response, err, calls.Load()-before)
			}
		})
	}
}

func TestNotificationTLSRejectsWildcardWSEvenWhenHostnameVerificationMatches(t *testing.T) {
	const wsName, pushName = "ws.go-im.internal", "push.go-im.internal"
	ca := newTestCA(t)
	serverConfig, err := NewNotificationServerTLSConfig(issueTestCertificate(t, ca, "*.go-im.internal", x509.ExtKeyUsageServerAuth, false), pushName)
	if err != nil {
		t.Fatal(err)
	}
	url, calls := serveNotificationTLSForTest(t, serverConfig, pushName)
	clientConfig, err := NewNotificationClientTLSConfig(issueTestCertificate(t, ca, pushName, x509.ExtKeyUsageClientAuth, false), wsName)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{TLSClientConfig: clientConfig}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Post(url, "application/json", strings.NewReader("{}"))
	if response != nil {
		defer response.Body.Close()
	}
	if err == nil || calls.Load() != 0 {
		t.Fatalf("wildcard WS was treated as exact service: error=%v calls=%d", err, calls.Load())
	}
}

func TestVerifyNotificationPeerRequiresVerifiedChainAndExactSAN(t *testing.T) {
	const name = "push.go-im.internal"
	leaf := &x509.Certificate{DNSNames: []string{name}}
	verified := tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
	// VerifyConnection runs before HandshakeComplete is set; HTTP handlers check it separately.
	if err := VerifyNotificationPeer(&verified, name); err != nil {
		t.Fatal(err)
	}
	for _, state := range []*tls.ConnectionState{
		nil, {}, {PeerCertificates: []*x509.Certificate{leaf}}, {VerifiedChains: verified.VerifiedChains},
		{PeerCertificates: []*x509.Certificate{{DNSNames: []string{"*.go-im.internal"}}}, VerifiedChains: verified.VerifiedChains},
		{PeerCertificates: []*x509.Certificate{{Subject: pkix.Name{CommonName: name}, DNSNames: []string{"other.go-im.internal"}}}, VerifiedChains: verified.VerifiedChains},
	} {
		if err := VerifyNotificationPeer(state, name); err == nil {
			t.Fatal("unverified chain, common name or wildcard accepted")
		}
	}
	for _, name := range []string{"", " push.go-im.internal", "push.go-im.internal ", "*.go-im.internal", "push.go-im.internal:443", "push/go-im.internal"} {
		if err := VerifyNotificationPeer(&verified, name); err == nil {
			t.Fatalf("ambiguous identity accepted: %q", name)
		}
	}
}

func TestNotificationTLSRejectsIncompleteFilesAndAmbiguousIdentityWithoutFallback(t *testing.T) {
	ca := newTestCA(t)
	files := issueTestCertificate(t, ca, "push.go-im.internal", x509.ExtKeyUsageClientAuth, false)
	other := issueTestCertificate(t, ca, "other.go-im.internal", x509.ExtKeyUsageClientAuth, false)
	invalidCA := filepath.Join(t.TempDir(), "invalid-ca.pem")
	if err := os.WriteFile(invalidCA, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []CertificateFiles{
		{}, {CertFile: files.CertFile, KeyFile: files.KeyFile}, {KeyFile: files.KeyFile, CAFile: files.CAFile},
		{CertFile: files.CertFile, CAFile: files.CAFile},
		{CertFile: files.CertFile + ".missing", KeyFile: files.KeyFile, CAFile: files.CAFile},
		{CertFile: files.CertFile, KeyFile: other.KeyFile, CAFile: files.CAFile},
		{CertFile: files.CertFile, KeyFile: files.KeyFile, CAFile: invalidCA},
	} {
		if config, err := NewNotificationClientTLSConfig(bad, "ws.go-im.internal"); err == nil || config != nil {
			t.Fatal("client returned a usable or plaintext fallback for invalid files")
		}
		if config, err := NewNotificationServerTLSConfig(bad, "push.go-im.internal"); err == nil || config != nil {
			t.Fatal("server returned a usable or plaintext fallback for invalid files")
		}
	}
	for _, name := range []string{"", " ", "*.go-im.internal", "push.go-im.internal:443", "push/go-im.internal", "push@go-im.internal"} {
		if config, err := NewNotificationClientTLSConfig(files, name); err == nil || config != nil {
			t.Fatalf("client accepted ambiguous identity %q", name)
		}
		if config, err := NewNotificationServerTLSConfig(files, name); err == nil || config != nil {
			t.Fatalf("server accepted ambiguous identity %q", name)
		}
	}
}
