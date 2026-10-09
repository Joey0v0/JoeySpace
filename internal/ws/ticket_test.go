package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/yjydist/go-im/internal/pkg/jwt"
	"github.com/yjydist/go-im/internal/repository"
	"go.uber.org/zap"
)

type testTicketStore struct {
	mu      sync.Mutex
	entries map[string]struct {
		id    int64
		token string
	}
	issuedTTL time.Duration
	consumes  int
}

func (s *testTicketStore) Issue(_ context.Context, id int64, token string, ttl time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]struct {
			id    int64
			token string
		})
	}
	s.issuedTTL = ttl
	s.entries["single-use"] = struct {
		id    int64
		token string
	}{id, token}
	return "single-use", nil
}

func (s *testTicketStore) Consume(_ context.Context, ticket string) (int64, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumes++
	entry, ok := s.entries[ticket]
	if !ok {
		return 0, "", repository.ErrWSTicketMissing
	}
	delete(s.entries, ticket)
	return entry.id, entry.token, nil
}

type ticketTestRedis struct{ repository.RedisRepository }

func (ticketTestRedis) SetOnline(context.Context, int64, string, time.Duration) error { return nil }
func (ticketTestRedis) DelOnline(context.Context, int64) error                        { return nil }

func TestWSTicketSingleUseAndOrigin(t *testing.T) {
	const secret = "ticket-test-secret"
	token, err := jwt.GenerateToken(42, secret, 1)
	if err != nil {
		t.Fatal(err)
	}
	store := &testTicketStore{}
	hub := NewHub(zap.NewNop())
	go hub.Run()
	server := &Server{hub: hub, redisRepo: ticketTestRedis{}, tickets: store, jwtSecret: secret, logger: zap.NewNop(), allowedOrigins: map[string]struct{}{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws-ticket", server.HandleWSTicket)
	mux.HandleFunc("/ws", server.HandleWS)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	issue := func(origin, bearer string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/ws-ticket", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", bearer)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	for _, item := range []struct {
		origin, bearer string
		status         int
	}{
		{"https://evil.example", "Bearer " + token, http.StatusForbidden},
		{"", "Bearer invalid", http.StatusUnauthorized},
	} {
		response := issue(item.origin, item.bearer)
		response.Body.Close()
		if response.StatusCode != item.status {
			t.Fatalf("issue status = %d, want %d", response.StatusCode, item.status)
		}
	}
	response := issue(httpServer.URL, "Bearer "+token)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("issue response = %d, cache=%q", response.StatusCode, response.Header.Get("Cache-Control"))
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			Ticket  string `json:"ticket"`
			Expires int    `json:"expires_in_seconds"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != 0 || body.Data.Ticket != "single-use" || body.Data.Expires != 30 || store.issuedTTL != wsTicketTTL {
		t.Fatalf("bad issue result: %+v", body)
	}

	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws?ticket=" + body.Data.Ticket
	dialer := websocket.Dialer{}
	badOrigin := http.Header{"Origin": []string{"https://evil.example"}}
	_, rejection, err := dialer.Dial(wsURL, badOrigin)
	if err == nil || rejection == nil || rejection.StatusCode != http.StatusForbidden {
		t.Fatalf("bad origin = %v, %v", rejection, err)
	}
	rejection.Body.Close()
	if store.consumes != 0 {
		t.Fatal("forbidden origin consumed ticket")
	}

	conn, handshake, err := dialer.Dial(wsURL, http.Header{"Origin": []string{httpServer.URL}})
	if err != nil {
		t.Fatalf("ticket handshake: %v, %v", handshake, err)
	}
	conn.Close()
	_, replay, err := dialer.Dial(wsURL, http.Header{"Origin": []string{httpServer.URL}})
	if err == nil || replay == nil || replay.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replay = %v, %v", replay, err)
	}
	replay.Body.Close()
	if store.consumes != 2 {
		t.Fatalf("consume count = %d", store.consumes)
	}
}
