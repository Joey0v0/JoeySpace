package ws

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type TaskNotificationTeamChecker interface {
	CheckTeamMember(context.Context, *userpb.CheckTeamMemberRequest, ...grpc.CallOption) (*userpb.CheckTeamMemberResponse, error)
}

// NewTaskNotificationHandler accepts refresh hints only from the verified Push service.
func NewTaskNotificationHandler(hub *Hub, teams TaskNotificationTeamChecker, pushDNSName string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fail := func(code int) { http.Error(w, "task notification delivery unavailable", code) }
		if r.TLS == nil || !r.TLS.HandshakeComplete || rpcauth.VerifyNotificationPeer(r.TLS, pushDNSName) != nil {
			http.Error(w, "verified notification Push service required", http.StatusForbidden)
			return
		}
		if r.URL.Path != model.TaskNotificationPushPath || r.URL.EscapedPath() != model.TaskNotificationPushPath {
			http.Error(w, "notification endpoint not found", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery || len(r.Header.Values("Authorization")) != 0 {
			http.Error(w, "invalid notification request", http.StatusBadRequest)
			return
		}
		if hub == nil || teams == nil {
			fail(http.StatusServiceUnavailable)
			return
		}
		if r.Body == nil {
			http.Error(w, "invalid notification event", http.StatusBadRequest)
			return
		}
		wire, err := io.ReadAll(http.MaxBytesReader(w, r.Body, model.TaskNotificationMaxWireBytes))
		if err != nil {
			http.Error(w, "invalid notification event", http.StatusBadRequest)
			return
		}
		event, err := model.DecodeTaskNotificationEvent(wire)
		if err != nil {
			http.Error(w, "invalid notification event", http.StatusBadRequest)
			return
		}
		contextFailure := func(ctx context.Context) bool {
			if ctx.Err() == nil {
				return false
			}
			code := http.StatusServiceUnavailable
			if ctx.Err() == context.DeadlineExceeded {
				code = http.StatusGatewayTimeout
			}
			fail(code)
			return true
		}
		if contextFailure(r.Context()) {
			return
		}
		respond := func(outcome string) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(model.TaskNotificationDelivery{NotificationID: event.NotificationID, Outcome: outcome})
		}
		client, online := hub.GetClient(event.RecipientID)
		if !online {
			respond(model.TaskNotificationOffline)
			return
		}
		if client == nil || client.UserID != event.RecipientID || strings.TrimSpace(client.token) == "" {
			fail(http.StatusServiceUnavailable)
			return
		}
		teamCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		teamCtx = metadata.NewOutgoingContext(teamCtx, metadata.Pairs("authorization", "Bearer "+client.token))
		member, err := teams.CheckTeamMember(teamCtx, &userpb.CheckTeamMemberRequest{TeamId: event.TeamID})
		if contextFailure(teamCtx) {
			return
		}
		if err != nil {
			switch status.Code(err) {
			case codes.Unauthenticated, codes.PermissionDenied:
				respond(model.TaskNotificationDenied)
			case codes.DeadlineExceeded:
				fail(http.StatusGatewayTimeout)
			default:
				fail(http.StatusServiceUnavailable)
			}
			return
		}
		if member.GetUserId() <= 0 || member.GetUserId() != client.UserID || member.GetUserId() != event.RecipientID || member.GetRole() < 0 || member.GetRole() > 2 {
			fail(http.StatusServiceUnavailable)
			return
		}
		payload, err := json.Marshal(ServerMsg{Type: model.TaskNotificationEventType, Data: struct {
			Version        int   `json:"version"`
			NotificationID int64 `json:"notification_id,string"`
			TeamID         int64 `json:"team_id,string"`
		}{event.Version, event.NotificationID, event.TeamID}})
		if err != nil {
			fail(http.StatusServiceUnavailable)
			return
		}
		hub.mu.RLock()
		queued := teamCtx.Err() == nil && hub.clients[event.RecipientID] == client && queueTaskNotification(client, payload)
		hub.mu.RUnlock()
		if contextFailure(teamCtx) {
			return
		}
		if !queued {
			fail(http.StatusServiceUnavailable)
			return
		}
		respond(model.TaskNotificationQueued)
	})
}

// The caller holds Hub.mu while checking the same client. Avoid Client.Send's
// full-queue branch, which unregisters/closes and could block while holding that lock.
func queueTaskNotification(client *Client, payload []byte) bool {
	select {
	case <-client.closeCh:
		return false
	default:
	}
	select {
	case <-client.closeCh:
		return false
	case client.send <- payload:
		return true
	default:
		return false
	}
}
