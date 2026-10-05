package ws

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const notificationPushName = "push.joeyspace.internal"

type notificationTeamsStub func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error)

func (f notificationTeamsStub) CheckTeamMember(ctx context.Context, req *userpb.CheckTeamMemberRequest, _ ...grpc.CallOption) (*userpb.CheckTeamMemberResponse, error) {
	return f(ctx, req)
}

func notificationHandlerFixture() (*Hub, *Client, model.TaskNotificationEvent) {
	hub := NewHub(zap.NewNop())
	client := &Client{UserID: 42, token: "private-connection-token", send: make(chan []byte, 2), closeCh: make(chan struct{}), hub: hub, logger: zap.NewNop()}
	hub.clients[42] = client
	return hub, client, model.TaskNotificationEvent{Version: 1, Type: model.TaskNotificationEventType, NotificationID: math.MaxInt64, TeamID: math.MaxInt64 - 1, RecipientID: 42}
}

func notificationRequest(t *testing.T, event model.TaskNotificationEvent) *http.Request {
	t.Helper()
	wire, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, model.TaskNotificationPushPath, strings.NewReader(string(wire)))
	leaf := &x509.Certificate{DNSNames: []string{notificationPushName}}
	req.TLS = &tls.ConnectionState{HandshakeComplete: true, PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
	return req
}

func decodeNotificationDelivery(t *testing.T, recorder *httptest.ResponseRecorder, expectedID int64, expectedOutcome string) {
	t.Helper()
	var result model.TaskNotificationDelivery
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &result) != nil || result.Validate(expectedID) != nil || result.Outcome != expectedOutcome {
		t.Fatalf("HTTP delivery=%d %s", recorder.Code, recorder.Body.String())
	}
	var fields map[string]any
	if json.Unmarshal(recorder.Body.Bytes(), &fields) != nil || len(fields) != 2 || fields["notification_id"] != "9223372036854775807" {
		t.Fatalf("unexpected response fields=%v", fields)
	}
}

func TestTaskNotificationHandlerQueuesMinimalHintUsingOnlyCurrentConnectionToken(t *testing.T) {
	hub, client, event := notificationHandlerFixture()
	calls := 0
	teams := notificationTeamsStub(func(ctx context.Context, req *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
		calls++
		md, _ := metadata.FromOutgoingContext(ctx)
		if len(md) != 1 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer private-connection-token" || req.GetTeamId() != event.TeamID {
			t.Errorf("membership request=%v metadata=%v", req, md)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second || time.Until(deadline) <= 0 {
			t.Errorf("unbounded membership call deadline=%v", deadline)
		}
		return &userpb.CheckTeamMemberResponse{UserId: 42, Role: 2}, nil
	})
	req := notificationRequest(t, event)
	req = req.WithContext(metadata.NewOutgoingContext(req.Context(), metadata.Pairs("authorization", "Bearer attacker", "recipient-id", "77")))
	recorder := httptest.NewRecorder()
	NewTaskNotificationHandler(hub, teams, notificationPushName).ServeHTTP(recorder, req)
	decodeNotificationDelivery(t, recorder, event.NotificationID, model.TaskNotificationQueued)
	if calls != 1 || len(client.send) != 1 {
		t.Fatalf("membership=%d queue=%d", calls, len(client.send))
	}
	wire := <-client.send
	var message struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(wire, &message); err != nil || message.Type != model.TaskNotificationEventType || len(message.Data) != 3 ||
		message.Data["version"] != float64(1) || message.Data["notification_id"] != "9223372036854775807" || message.Data["team_id"] != "9223372036854775806" {
		t.Fatalf("queued hint=%s error=%v", wire, err)
	}
	for _, private := range []string{"recipient", "private-connection-token", "attacker", "content", "task_id"} {
		if strings.Contains(string(wire), private) || strings.Contains(recorder.Body.String(), private) {
			t.Fatalf("private field leaked: %s", private)
		}
	}
}

func TestTaskNotificationHandlerOfflineDoesNotQueryMembership(t *testing.T) {
	hub, client, event := notificationHandlerFixture()
	delete(hub.clients, event.RecipientID)
	teams := notificationTeamsStub(func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
		t.Error("offline event queried User")
		return nil, nil
	})
	recorder := httptest.NewRecorder()
	NewTaskNotificationHandler(hub, teams, notificationPushName).ServeHTTP(recorder, notificationRequest(t, event))
	decodeNotificationDelivery(t, recorder, event.NotificationID, model.TaskNotificationOffline)
	if len(client.send) != 0 {
		t.Fatal("offline event sent a hint")
	}
}

func TestTaskNotificationHandlerRevokedOrExpiredConnectionIsDeniedWithoutQueueing(t *testing.T) {
	for _, code := range []codes.Code{codes.PermissionDenied, codes.Unauthenticated} {
		t.Run(code.String(), func(t *testing.T) {
			hub, client, event := notificationHandlerFixture()
			teams := notificationTeamsStub(func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
				return nil, status.Error(code, "private Token or membership detail")
			})
			recorder := httptest.NewRecorder()
			NewTaskNotificationHandler(hub, teams, notificationPushName).ServeHTTP(recorder, notificationRequest(t, event))
			decodeNotificationDelivery(t, recorder, event.NotificationID, model.TaskNotificationDenied)
			if len(client.send) != 0 || strings.Contains(recorder.Body.String(), "private") {
				t.Fatal("denied event queued or leaked detail")
			}
		})
	}
}

func TestTaskNotificationHandlerTransientUserFailuresAreRetryable(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.Internal, codes.NotFound, codes.FailedPrecondition, codes.Canceled, codes.DeadlineExceeded} {
		t.Run(code.String(), func(t *testing.T) {
			hub, client, event := notificationHandlerFixture()
			teams := notificationTeamsStub(func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
				return nil, status.Error(code, "private service detail")
			})
			recorder := httptest.NewRecorder()
			NewTaskNotificationHandler(hub, teams, notificationPushName).ServeHTTP(recorder, notificationRequest(t, event))
			want := http.StatusServiceUnavailable
			if code == codes.DeadlineExceeded {
				want = http.StatusGatewayTimeout
			}
			if recorder.Code != want || len(client.send) != 0 || strings.Contains(recorder.Body.String(), "private") {
				t.Fatalf("temporary result=%d %s queue=%d", recorder.Code, recorder.Body.String(), len(client.send))
			}
		})
	}
}

func TestTaskNotificationHandlerRejectsMalformedMembershipResponses(t *testing.T) {
	for _, member := range []*userpb.CheckTeamMemberResponse{nil, {}, {UserId: -1}, {UserId: 77}, {UserId: 42, Role: -1}, {UserId: 42, Role: 3}} {
		hub, client, event := notificationHandlerFixture()
		teams := notificationTeamsStub(func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
			return member, nil
		})
		recorder := httptest.NewRecorder()
		NewTaskNotificationHandler(hub, teams, notificationPushName).ServeHTTP(recorder, notificationRequest(t, event))
		if recorder.Code != http.StatusServiceUnavailable || len(client.send) != 0 {
			t.Fatalf("invalid User response=%v HTTP=%d queue=%d", member, recorder.Code, len(client.send))
		}
	}
}

func TestTaskNotificationHandlerMembershipRunsWithoutHubLockAndRejectsReplacement(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		hub, old, event := notificationHandlerFixture()
		next := &Client{UserID: 42, token: "new-token", send: make(chan []byte, 1), closeCh: make(chan struct{})}
		teams := notificationTeamsStub(func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
			// A held Hub read lock here would deadlock; networking must happen before the short enqueue lock.
			hub.mu.Lock()
			if replacement {
				hub.clients[42] = next
			} else {
				delete(hub.clients, 42)
			}
			hub.mu.Unlock()
			return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
		})
		recorder := httptest.NewRecorder()
		NewTaskNotificationHandler(hub, teams, notificationPushName).ServeHTTP(recorder, notificationRequest(t, event))
		if recorder.Code != http.StatusServiceUnavailable || len(old.send) != 0 || len(next.send) != 0 {
			t.Fatalf("replacement HTTP=%d old/new queues=%d/%d", recorder.Code, len(old.send), len(next.send))
		}
	}
}

func TestTaskNotificationHandlerFullQueueAndUnregisterNeverBlockOrCloseConnection(t *testing.T) {
	hub, client, event := notificationHandlerFixture()
	for len(client.send) < cap(client.send) {
		client.send <- []byte("old message")
	}
	for len(hub.unregister) < cap(hub.unregister) {
		hub.unregister <- client
	}
	teams := notificationTeamsStub(func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
		return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
	})
	recorder, done := httptest.NewRecorder(), make(chan struct{})
	req := notificationRequest(t, event)
	go func() {
		NewTaskNotificationHandler(hub, teams, notificationPushName).ServeHTTP(recorder, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("full queue/unregister blocked handler")
	}
	if recorder.Code != http.StatusServiceUnavailable || len(client.send) != cap(client.send) {
		t.Fatalf("full queue result=%d queue=%d", recorder.Code, len(client.send))
	}
	select {
	case <-client.closeCh:
		t.Fatal("hint delivery closed the chat connection")
	default:
	}
}

func TestTaskNotificationHandlerClosedConnectionDoesNotQueue(t *testing.T) {
	hub, client, event := notificationHandlerFixture()
	close(client.closeCh)
	teams := notificationTeamsStub(func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
		return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
	})
	recorder := httptest.NewRecorder()
	NewTaskNotificationHandler(hub, teams, notificationPushName).ServeHTTP(recorder, notificationRequest(t, event))
	if recorder.Code != http.StatusServiceUnavailable || len(client.send) != 0 {
		t.Fatalf("closed connection HTTP=%d queue=%d", recorder.Code, len(client.send))
	}
}

func TestTaskNotificationHandlerRejectsUnverifiedIncompleteOrWrongTLSIdentity(t *testing.T) {
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.TLS = nil },
		func(r *http.Request) { r.TLS.HandshakeComplete = false },
		func(r *http.Request) { r.TLS.VerifiedChains = nil },
		func(r *http.Request) { r.TLS.PeerCertificates = nil },
		func(r *http.Request) { r.TLS.PeerCertificates[0].DNSNames = []string{"another-service.internal"} },
		func(r *http.Request) { r.TLS.PeerCertificates[0].DNSNames = []string{"*.joeyspace.internal"} },
		func(r *http.Request) {
			r.TLS.PeerCertificates[0].DNSNames = nil
			r.TLS.PeerCertificates[0].Subject.CommonName = notificationPushName
		},
	} {
		hub, client, event := notificationHandlerFixture()
		teams := notificationTeamsStub(func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
			t.Error("unverified request reached User")
			return nil, nil
		})
		req := notificationRequest(t, event)
		mutate(req)
		recorder := httptest.NewRecorder()
		NewTaskNotificationHandler(hub, teams, notificationPushName).ServeHTTP(recorder, req)
		if recorder.Code != http.StatusForbidden || len(client.send) != 0 {
			t.Fatalf("TLS rejection=%d queue=%d", recorder.Code, len(client.send))
		}
	}
}

func TestTaskNotificationHandlerRejectsUnexpectedHTTPShapeBeforeUser(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*http.Request)
		code   int
	}{
		{"GET", func(r *http.Request) { r.Method = http.MethodGet }, http.StatusMethodNotAllowed},
		{"old push path", func(r *http.Request) { r.URL.Path = "/internal/push" }, http.StatusNotFound},
		{"query scope", func(r *http.Request) { r.URL.RawQuery = "recipient_id=77" }, http.StatusBadRequest},
		{"empty query", func(r *http.Request) { r.URL.ForceQuery = true }, http.StatusBadRequest},
		{"injected Bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer private-attacker-token") }, http.StatusBadRequest},
		{"empty Bearer header", func(r *http.Request) { r.Header["Authorization"] = []string{""} }, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub, client, event := notificationHandlerFixture()
			teams := notificationTeamsStub(func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
				t.Error("malformed request reached User")
				return nil, nil
			})
			req := notificationRequest(t, event)
			tc.mutate(req)
			recorder := httptest.NewRecorder()
			NewTaskNotificationHandler(hub, teams, notificationPushName).ServeHTTP(recorder, req)
			if recorder.Code != tc.code || len(client.send) != 0 || strings.Contains(recorder.Body.String(), "attacker") {
				t.Fatalf("request rejection=%d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestTaskNotificationHandlerRejectsInvalidOrOversizedEventsBeforeUser(t *testing.T) {
	for _, wire := range []string{
		"", "{}", strings.Repeat("x", model.TaskNotificationMaxWireBytes+1),
		`{"version":1,"type":"task_notification_changed","notification_id":"1","team_id":"2","recipient_id":"42","token":"private"}`,
		`{"version":1,"type":"task_notification_changed","notification_id":"1","team_id":"2","recipient_id":42}`,
		`{"version":1,"type":"task_notification_changed","notification_id":"01","team_id":"2","recipient_id":"42"}`,
		`{"version":1,"version":1,"type":"task_notification_changed","notification_id":"1","team_id":"2","recipient_id":"42"}`,
		`{"version":2,"type":"task_notification_changed","notification_id":"1","team_id":"2","recipient_id":"42"}`,
		`{"version":1,"type":"task_notification_changed","notification_id":"1","team_id":"2","recipient_id":"42"} {}`,
	} {
		hub, client, event := notificationHandlerFixture()
		teams := notificationTeamsStub(func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
			t.Error("bad event reached User")
			return nil, nil
		})
		req := notificationRequest(t, event)
		req.Body = http.NoBody
		if wire != "" {
			req = req.Clone(req.Context())
			req.Body = io.NopCloser(strings.NewReader(wire))
		}
		recorder := httptest.NewRecorder()
		NewTaskNotificationHandler(hub, teams, notificationPushName).ServeHTTP(recorder, req)
		if recorder.Code != http.StatusBadRequest || len(client.send) != 0 || strings.Contains(recorder.Body.String(), "private") {
			t.Fatalf("bad event result=%d %s", recorder.Code, recorder.Body.String())
		}
	}
}

func TestTaskNotificationHandlerMissingDependenciesAndInvalidConnectionsFailSafely(t *testing.T) {
	hub, client, event := notificationHandlerFixture()
	goodTeams := notificationTeamsStub(func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
		return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
	})
	for _, handler := range []http.Handler{NewTaskNotificationHandler(nil, goodTeams, notificationPushName), NewTaskNotificationHandler(hub, nil, notificationPushName)} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, notificationRequest(t, event))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("missing dependency HTTP=%d", recorder.Code)
		}
	}
	for _, badClient := range []*Client{nil, {UserID: 77, token: "private"}, {UserID: 42, token: ""}} {
		hub.clients[42] = badClient
		recorder := httptest.NewRecorder()
		NewTaskNotificationHandler(hub, goodTeams, notificationPushName).ServeHTTP(recorder, notificationRequest(t, event))
		if recorder.Code != http.StatusServiceUnavailable || len(client.send) != 0 {
			t.Fatalf("invalid connection HTTP=%d", recorder.Code)
		}
	}
}

func TestTaskNotificationHandlerCanceledAndExpiredRequestsNeverQueue(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		hub, client, event := notificationHandlerFixture()
		teams := notificationTeamsStub(func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
			t.Error("expired request reached User")
			return nil, nil
		})
		var ctx context.Context
		var cancel context.CancelFunc
		if deadline {
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		} else {
			ctx, cancel = context.WithCancel(context.Background())
			cancel()
		}
		defer cancel()
		recorder := httptest.NewRecorder()
		NewTaskNotificationHandler(hub, teams, notificationPushName).ServeHTTP(recorder, notificationRequest(t, event).WithContext(ctx))
		want := http.StatusServiceUnavailable
		if deadline {
			want = http.StatusGatewayTimeout
		}
		if recorder.Code != want || len(client.send) != 0 {
			t.Fatalf("expired request HTTP=%d queue=%d", recorder.Code, len(client.send))
		}
	}
}

func TestTaskNotificationHandlerMembershipTimeoutNeverQueuesEvenIfUserReturnsSuccess(t *testing.T) {
	hub, client, event := notificationHandlerFixture()
	teams := notificationTeamsStub(func(ctx context.Context, _ *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
		<-ctx.Done()
		return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	recorder := httptest.NewRecorder()
	NewTaskNotificationHandler(hub, teams, notificationPushName).ServeHTTP(recorder, notificationRequest(t, event).WithContext(ctx))
	if recorder.Code != http.StatusGatewayTimeout || len(client.send) != 0 {
		t.Fatalf("membership deadline HTTP=%d queue=%d", recorder.Code, len(client.send))
	}
}

func notificationTLSFiles(t *testing.T) (rpcauth.CertificateFiles, rpcauth.CertificateFiles) {
	t.Helper()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test notification root"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		t.Fatal(err)
	}
	issue := func(name string, serial int64, purpose x509.ExtKeyUsage) rpcauth.CertificateFiles {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{name},
			NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{purpose}}
		der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, rootKey)
		if err != nil {
			t.Fatal(err)
		}
		private, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		files := rpcauth.CertificateFiles{CertFile: filepath.Join(dir, name+".pem"), KeyFile: filepath.Join(dir, name+".key"), CAFile: caPath}
		if err := os.WriteFile(files.CertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(files.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}), 0600); err != nil {
			t.Fatal(err)
		}
		return files
	}
	return issue("ws.joeyspace.internal", 2, x509.ExtKeyUsageServerAuth), issue(notificationPushName, 3, x509.ExtKeyUsageClientAuth)
}

func TestTaskNotificationHandlerOverRealMutualTLS(t *testing.T) {
	hub, client, event := notificationHandlerFixture()
	teams := notificationTeamsStub(func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
		return &userpb.CheckTeamMemberResponse{UserId: 42, Role: 1}, nil
	})
	serverFiles, pushFiles := notificationTLSFiles(t)
	serverTLS, err := rpcauth.NewNotificationServerTLSConfig(serverFiles, notificationPushName)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(NewTaskNotificationHandler(hub, teams, notificationPushName))
	server.TLS = serverTLS
	server.StartTLS()
	defer server.Close()
	clientTLS, err := rpcauth.NewNotificationClientTLSConfig(pushFiles, "ws.joeyspace.internal")
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{TLSClientConfig: clientTLS}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	wire, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	response, err := httpClient.Post(server.URL+model.TaskNotificationPushPath, "application/json", strings.NewReader(string(wire)))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var delivery model.TaskNotificationDelivery
	if err := json.NewDecoder(response.Body).Decode(&delivery); err != nil || response.StatusCode != http.StatusOK || delivery.Validate(event.NotificationID) != nil || delivery.Outcome != model.TaskNotificationQueued || len(client.send) != 1 {
		t.Fatalf("mTLS delivery=%v status=%d error=%v queue=%d", delivery, response.StatusCode, err, len(client.send))
	}
}
