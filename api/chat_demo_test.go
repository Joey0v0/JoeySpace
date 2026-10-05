package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/examples"
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

func TestMultiDraftDemoServesSameOriginEmbeddedScripts(t *testing.T) {
	for _, asset := range []struct{ path, body string }{
		{"/demo/multi-draft-core.js", examples.MultiDraftCoreJS},
		{"/demo/multi-draft-actions.js", examples.MultiDraftActionsJS},
		{"/demo/multi-draft-view.js", examples.MultiDraftViewJS},
	} {
		t.Run(asset.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			chatDemoScriptHandler(asset.body)(w, httptest.NewRequest(http.MethodGet, asset.path, nil))
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/javascript; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" ||
				asset.body == "" || w.Body.String() != asset.body || !strings.Contains(examples.ChatHTML, `src="`+asset.path+`"`) {
				t.Fatalf("missing same-origin embedded script: status=%d type=%q", w.Code, w.Header().Get("Content-Type"))
			}
		})
	}
}

func TestTaskNotificationDemoServesSameOriginEmbeddedScripts(t *testing.T) {
	for _, asset := range []struct{ path, body string }{
		{"/demo/task-notifications.js", examples.TaskNotificationsJS},
		{"/demo/task-notifications-view.js", examples.TaskNotificationsViewJS},
	} {
		t.Run(asset.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			chatDemoScriptHandler(asset.body)(w, httptest.NewRequest(http.MethodGet, asset.path, nil))
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/javascript; charset=utf-8" ||
				w.Header().Get("Cache-Control") != "no-store" || asset.body == "" || w.Body.String() != asset.body ||
				!strings.Contains(examples.ChatHTML, `src="`+asset.path+`"`) {
				t.Fatalf("missing notification script: status=%d type=%q", w.Code, w.Header().Get("Content-Type"))
			}
		})
	}
}
