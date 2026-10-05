package push

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
)

type taskNotificationRoundTripFunc func(*http.Request) (*http.Response, error)

func (f taskNotificationRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func taskNotificationTestFiles(t *testing.T) rpcauth.CertificateFiles {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	if len(server.TLS.Certificates) != 1 || len(server.TLS.Certificates[0].Certificate) == 0 {
		t.Fatal("TLS fixture has no certificate")
	}
	certificate := server.TLS.Certificates[0]
	keyDER, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := rpcauth.CertificateFiles{
		CertFile: filepath.Join(dir, "push-cert.pem"),
		KeyFile:  filepath.Join(dir, "push-key.pem"),
		CAFile:   filepath.Join(dir, "notification-ca.pem"),
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	for path, content := range map[string][]byte{files.CertFile: certPEM, files.KeyFile: keyPEM, files.CAFile: certPEM} {
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func taskNotificationTestEvent() model.TaskNotificationEvent {
	return model.TaskNotificationEvent{
		Version: model.TaskNotificationEventVersion, Type: model.TaskNotificationEventType,
		NotificationID: 9007199254740997, TeamID: 9007199254740993, RecipientID: 9007199254740995,
	}
}

func taskNotificationTestClient(t *testing.T) *TaskNotificationClient {
	t.Helper()
	client, err := NewTaskNotificationClient(TaskNotificationClientConfig{
		Files: taskNotificationTestFiles(t), WSDNSName: "ws.internal",
		Routes: map[string]string{"im-ws:9091": "https://ws.internal:9443"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func taskNotificationTestResponse(statusCode int, body string) *http.Response {
	return &http.Response{StatusCode: statusCode, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestTaskNotificationClientCopiesAllowlistAndSendsOnlyEvent(t *testing.T) {
	routes := map[string]string{"im-ws:9091": "https://ws.internal:9443"}
	client, err := NewTaskNotificationClient(TaskNotificationClientConfig{Files: taskNotificationTestFiles(t), WSDNSName: "ws.internal", Routes: routes})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	routes["im-ws:9091"] = "https://changed.internal:9443"
	routes["evil:9091"] = "https://evil.internal:9443"
	calls := 0
	client.client.Transport = taskNotificationRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodPost || req.URL.String() != "https://ws.internal:9443"+model.TaskNotificationPushPath ||
			req.Header.Get("Content-Type") != "application/json" || req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" {
			t.Fatalf("unsafe request route, method or headers: %s %s %v", req.Method, req.URL, req.Header)
		}
		wire, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := model.DecodeTaskNotificationEvent(wire)
		if err != nil || decoded != taskNotificationTestEvent() || bytes.Contains(wire, []byte("token")) || len(wire) > model.TaskNotificationMaxWireBytes {
			t.Fatalf("wrong event body: %s, err=%v", wire, err)
		}
		return taskNotificationTestResponse(http.StatusOK, `{"notification_id":"9007199254740997","outcome":"queued"}`), nil
	})
	result, err := client.Send(context.Background(), "im-ws:9091", taskNotificationTestEvent())
	if err != nil || result.NotificationID != taskNotificationTestEvent().NotificationID || result.Outcome != model.TaskNotificationQueued || calls != 1 {
		t.Fatalf("send event: %+v, err=%v, calls=%d", result, err, calls)
	}
	if _, err := client.Send(context.Background(), "evil:9091", taskNotificationTestEvent()); !errors.Is(err, errTaskNotificationClientRoute) || calls != 1 {
		t.Fatalf("unmapped Redis address reached transport: %v, calls=%d", err, calls)
	}
	invalid := taskNotificationTestEvent()
	invalid.Version = 9
	if _, err := client.Send(context.Background(), "im-ws:9091", invalid); !errors.Is(err, errTaskNotificationClientRequest) || calls != 1 {
		t.Fatalf("invalid event reached transport: %v, calls=%d", err, calls)
	}
}

func TestTaskNotificationClientRejectsInvalidRoutesAndCredentials(t *testing.T) {
	files := taskNotificationTestFiles(t)
	for _, tc := range []struct {
		name, onlineAddr, origin string
	}{
		{"empty address", "", "https://ws.internal:9443"},
		{"URL as Redis address", "http://im-ws:9091", "https://ws.internal:9443"},
		{"path as Redis address", "im-ws:9091/path", "https://ws.internal:9443"},
		{"noncanonical Redis port", "im-ws:09091", "https://ws.internal:9443"},
		{"HTTP downgrade", "im-ws:9091", "http://ws.internal:9443"},
		{"missing HTTPS port", "im-ws:9091", "https://ws.internal"},
		{"URL user", "im-ws:9091", "https://user@ws.internal:9443"},
		{"URL path", "im-ws:9091", "https://ws.internal:9443/elsewhere"},
		{"URL slash", "im-ws:9091", "https://ws.internal:9443/"},
		{"URL query", "im-ws:9091", "https://ws.internal:9443?x=1"},
		{"URL fragment", "im-ws:9091", "https://ws.internal:9443#x"},
		{"URL invalid port", "im-ws:9091", "https://ws.internal:65536"},
		{"URL extra authority", "im-ws:9091", "https://ws.internal:9443@evil.internal:9443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewTaskNotificationClient(TaskNotificationClientConfig{
				Files: files, WSDNSName: "ws.internal", Routes: map[string]string{tc.onlineAddr: tc.origin},
			})
			if client != nil || !errors.Is(err, errTaskNotificationClientConfig) || strings.Contains(err.Error(), tc.origin) {
				t.Fatalf("unsafe route accepted: client=%v, err=%v", client, err)
			}
		})
	}
	for _, cfg := range []TaskNotificationClientConfig{
		{Files: files, WSDNSName: "ws.internal"},
		{Files: files, WSDNSName: "*.internal", Routes: map[string]string{"im-ws:9091": "https://ws.internal:9443"}},
		{Files: rpcauth.CertificateFiles{CertFile: "private-cert-path", KeyFile: "private-key-path", CAFile: "private-ca-path"},
			WSDNSName: "ws.internal", Routes: map[string]string{"im-ws:9091": "https://ws.internal:9443"}},
	} {
		client, err := NewTaskNotificationClient(cfg)
		if client != nil || !errors.Is(err, errTaskNotificationClientConfig) || strings.Contains(err.Error(), "private-") {
			t.Fatalf("unsafe client configuration: client=%v, err=%v", client, err)
		}
	}
}

func TestTaskNotificationClientAcceptsOnlyExactTwoFieldDelivery(t *testing.T) {
	for _, outcome := range []string{model.TaskNotificationQueued, model.TaskNotificationOffline, model.TaskNotificationDenied} {
		client := taskNotificationTestClient(t)
		client.client.Transport = taskNotificationRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return taskNotificationTestResponse(http.StatusOK, `{"notification_id":"9007199254740997","outcome":"`+outcome+`"}`), nil
		})
		result, err := client.Send(context.Background(), "im-ws:9091", taskNotificationTestEvent())
		if err != nil || result.Outcome != outcome {
			t.Fatalf("valid outcome %q: %+v, %v", outcome, result, err)
		}
	}
	for _, tc := range []struct {
		name string
		body string
	}{
		{"missing outcome", `{"notification_id":"9007199254740997"}`},
		{"unknown outcome", `{"notification_id":"9007199254740997","outcome":"received"}`},
		{"wrong ID", `{"notification_id":"9007199254740995","outcome":"queued"}`},
		{"numeric ID", `{"notification_id":9007199254740997,"outcome":"queued"}`},
		{"leading zero", `{"notification_id":"09007199254740997","outcome":"queued"}`},
		{"escaped digit", `{"notification_id":"900719925474099\u0037","outcome":"queued"}`},
		{"duplicate field", `{"notification_id":"9007199254740997","notification_id":"9007199254740997","outcome":"queued"}`},
		{"duplicate outcome", `{"notification_id":"9007199254740997","outcome":"queued","outcome":"denied"}`},
		{"unknown field", `{"notification_id":"9007199254740997","outcome":"queued","recipient_id":"3"}`},
		{"second JSON", `{"notification_id":"9007199254740997","outcome":"queued"}{}`},
		{"trailing text", `{"notification_id":"9007199254740997","outcome":"queued"} private`},
		{"invalid UTF8", string([]byte{0xff})},
		{"too large", strings.Repeat("x", model.TaskNotificationMaxWireBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := taskNotificationTestClient(t)
			client.client.Transport = taskNotificationRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return taskNotificationTestResponse(http.StatusOK, tc.body), nil
			})
			result, err := client.Send(context.Background(), "im-ws:9091", taskNotificationTestEvent())
			if result != (model.TaskNotificationDelivery{}) || !errors.Is(err, errTaskNotificationClientResponse) || strings.Contains(err.Error(), "private") {
				t.Fatalf("invalid response accepted: %+v, %v", result, err)
			}
		})
	}
}

func TestTaskNotificationClientRejectsRedirectNon200AndCanceledContext(t *testing.T) {
	for _, statusCode := range []int{http.StatusForbidden, http.StatusBadRequest, http.StatusServiceUnavailable, http.StatusFound} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			client := taskNotificationTestClient(t)
			calls := 0
			client.client.Transport = taskNotificationRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				response := taskNotificationTestResponse(statusCode, `{"notification_id":"9007199254740997","outcome":"denied"}`)
				response.Header.Set("Location", "http://private-redirect.internal/steal")
				return response, nil
			})
			result, err := client.Send(context.Background(), "im-ws:9091", taskNotificationTestEvent())
			if result != (model.TaskNotificationDelivery{}) || !errors.Is(err, errTaskNotificationClientTransfer) || calls != 1 || strings.Contains(err.Error(), "private") {
				t.Fatalf("non-200 was accepted or redirect followed: %+v, %v, calls=%d", result, err, calls)
			}
		})
	}
	client := taskNotificationTestClient(t)
	calls := 0
	client.client.Transport = taskNotificationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return taskNotificationTestResponse(http.StatusOK, `{"notification_id":"9007199254740997","outcome":"queued"}`), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := client.Send(ctx, "im-ws:9091", taskNotificationTestEvent()); result != (model.TaskNotificationDelivery{}) || !errors.Is(err, errTaskNotificationClientContext) || calls != 0 {
		t.Fatalf("canceled request was sent: %+v, %v, calls=%d", result, err, calls)
	}
	ctx, cancel = context.WithCancel(context.Background())
	client.client.Transport = taskNotificationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		cancel()
		return taskNotificationTestResponse(http.StatusOK, `{"notification_id":"9007199254740997","outcome":"queued"}`), nil
	})
	if result, err := client.Send(ctx, "im-ws:9091", taskNotificationTestEvent()); result != (model.TaskNotificationDelivery{}) || !errors.Is(err, errTaskNotificationClientContext) {
		t.Fatalf("fake success after cancellation: %+v, %v", result, err)
	}
}

func TestTaskNotificationClientRejectsBodyFailureAndCloseStopsNewSends(t *testing.T) {
	client := taskNotificationTestClient(t)
	client.client.Transport = taskNotificationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("private URL and certificate detail")
	})
	if _, err := client.Send(context.Background(), "im-ws:9091", taskNotificationTestEvent()); !errors.Is(err, errTaskNotificationClientTransfer) || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe network error: %v", err)
	}
	client.client.Transport = taskNotificationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: failingTaskNotificationBody{}}, nil
	})
	if _, err := client.Send(context.Background(), "im-ws:9091", taskNotificationTestEvent()); !errors.Is(err, errTaskNotificationClientTransfer) {
		t.Fatalf("body read failure accepted: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if result, err := client.Send(context.Background(), "im-ws:9091", taskNotificationTestEvent()); result != (model.TaskNotificationDelivery{}) || !errors.Is(err, errTaskNotificationClientClosed) {
		t.Fatalf("closed client sent event: %+v, %v", result, err)
	}
}

type failingTaskNotificationBody struct{}

func (failingTaskNotificationBody) Read([]byte) (int, error) {
	return 0, errors.New("private body detail")
}
func (failingTaskNotificationBody) Close() error { return nil }
