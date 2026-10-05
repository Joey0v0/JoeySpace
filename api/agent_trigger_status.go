package main

import (
	"context"
	"net/http"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/agent/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type agentTriggerStatusReader interface {
	GetTaskTriggerStatus(context.Context, *pb.GetTaskTriggerStatusRequest, ...grpc.CallOption) (*pb.GetTaskTriggerStatusResponse, error)
}

type agentTriggerStatusResponse struct {
	Code int                     `json:"code"`
	Msg  string                  `json:"msg"`
	Data *agentTriggerStatusData `json:"data,omitempty"`
}

type agentTriggerStatusData struct {
	MessageID int64  `json:"message_id,string"`
	TeamID    int64  `json:"team_id,string"`
	GroupID   int64  `json:"group_id,string"`
	Status    string `json:"status"`
	RunID     int64  `json:"run_id,string"`
}

func triggerStatusRPCError(w http.ResponseWriter, err error) {
	httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "AI trigger service error"
	switch status.Code(err) {
	case codes.InvalidArgument:
		httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid trigger request"
	case codes.Unauthenticated:
		httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
	case codes.PermissionDenied:
		httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "current trigger access required"
	case codes.NotFound:
		httpStatus, code, message = http.StatusNotFound, errcode.ErrNotFound, "AI trigger not found"
	case codes.Unavailable:
		httpStatus, message = http.StatusServiceUnavailable, "AI trigger service unavailable"
	case codes.DeadlineExceeded:
		httpStatus, message = http.StatusGatewayTimeout, "AI trigger service timeout"
	}
	httpx.WriteJson(w, httpStatus, agentTriggerStatusResponse{Code: code, Msg: message})
}

func validTriggerStatusResult(result *pb.GetTaskTriggerStatusResponse, messageID, teamID, groupID int64) bool {
	if result == nil || messageID <= 0 || teamID <= 0 || groupID <= 0 || result.GetMessageId() != messageID || result.GetTeamId() != teamID || result.GetGroupId() != groupID {
		return false
	}
	switch result.GetStatus() {
	case "queued", "running", "exhausted":
		return result.GetRunId() == 0
	case "completed":
		return result.GetRunId() > 0
	default:
		return false
	}
}

func getTaskTriggerStatusHandler(client agentTriggerStatusReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := draftToken(r)
		if !ok {
			httpx.WriteJson(w, http.StatusUnauthorized, agentTriggerStatusResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		vars := pathvar.Vars(r)
		teamID, validTeam := parseDraftRevision(vars["team_id"])
		groupID, validGroup := parseDraftRevision(vars["group_id"])
		messageID, validMessage := parseDraftRevision(vars["message_id"])
		if !validTeam || !validGroup || !validMessage {
			httpx.WriteJson(w, http.StatusBadRequest, agentTriggerStatusResponse{Code: errcode.ErrBadRequest, Msg: "invalid trigger scope or message ID"})
			return
		}
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", token))
		result, err := client.GetTaskTriggerStatus(ctx, &pb.GetTaskTriggerStatusRequest{MessageId: messageID})
		if err != nil {
			triggerStatusRPCError(w, err)
			return
		}
		if !validTriggerStatusResult(result, messageID, teamID, groupID) {
			httpx.WriteJson(w, http.StatusBadGateway, agentTriggerStatusResponse{Code: errcode.ErrInternal, Msg: "AI trigger service returned invalid status"})
			return
		}
		httpx.WriteJson(w, http.StatusOK, agentTriggerStatusResponse{Code: errcode.Success, Msg: "success", Data: &agentTriggerStatusData{
			MessageID: result.GetMessageId(), TeamID: result.GetTeamId(), GroupID: result.GetGroupId(), Status: result.GetStatus(), RunID: result.GetRunId(),
		}})
	}
}
