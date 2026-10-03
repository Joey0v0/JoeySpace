package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatDemoServesEmbeddedPage(t *testing.T) {
	w := httptest.NewRecorder()
	chatDemoHandler(w, httptest.NewRequest(http.MethodGet, "/demo/chat", nil))
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("response: status=%d content-type=%q", w.Code, w.Header().Get("Content-Type"))
	}
	for _, expected := range []string{
		`/api/v1/user/login`, `/api/v1/message/offline`, `/api/v1/message/offline/ack`,
		`/api/v1/teams/`, `doLoadTeamGroupHistory`, `Step 5 — Team Tasks`, `doCreateTask`, `doLoadTaskSource`,
	} {
		if !strings.Contains(w.Body.String(), expected) {
			t.Fatalf("embedded page missing %q", expected)
		}
	}
}
