package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type teamLeaveHTTPResponse struct {
	Code int                `json:"code"`
	Msg  string             `json:"msg"`
	Data *teamLeaveHTTPData `json:"data,omitempty"`
}

type teamLeaveHTTPData struct {
	OperationID string `json:"operation_id"`
	TeamID      string `json:"team_id"`
	Generation  string `json:"generation"`
	Status      int32  `json:"status"` // 0 pending cleanup, 1 completed.
}

func leaveTeamHandler(client pb.UserClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, teamID, ok := teamLeaveRequestScope(w, r)
		if !ok {
			return
		}
		if r.URL.RawQuery != "" {
			teamLeaveBadRequest(w)
			return
		}
		var body struct {
			RequestKey string `json:"request_key"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			teamLeaveBadRequest(w)
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF || !validTeamLeaveKey(body.RequestKey) {
			teamLeaveBadRequest(w)
			return
		}
		result, err := client.LeaveTeam(ctx, &pb.LeaveTeamRequest{TeamId: teamID, RequestKey: body.RequestKey})
		writeTeamLeaveResult(w, teamID, result, err, true)
	}
}

func getTeamLeaveOperationHandler(client pb.UserClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, teamID, ok := teamLeaveRequestScope(w, r)
		if !ok {
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) != 1 || len(query["request_key"]) != 1 || !validTeamLeaveKey(query.Get("request_key")) {
			teamLeaveBadRequest(w)
			return
		}
		result, err := client.GetTeamLeaveOperation(ctx, &pb.GetTeamLeaveOperationRequest{TeamId: teamID, RequestKey: query.Get("request_key")})
		writeTeamLeaveResult(w, teamID, result, err, false)
	}
}

func teamLeaveRequestScope(w http.ResponseWriter, r *http.Request) (ctx context.Context, teamID int64, ok bool) {
	headers := r.Header.Values("Authorization")
	if len(headers) != 1 {
		httpx.WriteJson(w, http.StatusUnauthorized, teamLeaveHTTPResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
		return nil, 0, false
	}
	parts := strings.Fields(headers[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		httpx.WriteJson(w, http.StatusUnauthorized, teamLeaveHTTPResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
		return nil, 0, false
	}
	teamID, err := strconv.ParseInt(pathvar.Vars(r)["team_id"], 10, 64)
	if err != nil || teamID <= 0 {
		teamLeaveBadRequest(w)
		return nil, 0, false
	}
	return metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1])), teamID, true
}

func validTeamLeaveKey(key string) bool {
	if len(key) < 1 || len(key) > 64 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

func teamLeaveBadRequest(w http.ResponseWriter) {
	httpx.WriteJson(w, http.StatusBadRequest, teamLeaveHTTPResponse{Code: errcode.ErrBadRequest, Msg: "invalid team leave request"})
}

func writeTeamLeaveResult(w http.ResponseWriter, teamID int64, result *pb.TeamLeaveOperationResponse, err error, requireCompleted bool) {
	if err != nil {
		httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "team service error"
		switch status.Code(err) {
		case codes.InvalidArgument:
			httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid team leave request"
		case codes.Unauthenticated:
			httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
		case codes.PermissionDenied:
			httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "team membership required"
		case codes.NotFound:
			httpStatus, code, message = http.StatusNotFound, errcode.ErrNotFound, "team leave operation not found"
		case codes.FailedPrecondition:
			httpStatus, code, message = http.StatusConflict, errcode.ErrTeamRoleConflict, "team leave is not allowed or needs review"
		case codes.AlreadyExists:
			httpStatus, code, message = http.StatusConflict, errcode.ErrTeamRoleConflict, "request key belongs to another team"
		case codes.Unavailable:
			httpStatus, message = http.StatusServiceUnavailable, "team leave service unavailable; query status and retry with the same key"
		case codes.DeadlineExceeded, codes.Canceled:
			httpStatus, message = http.StatusGatewayTimeout, "team leave timed out; query status before retry"
		}
		httpx.WriteJson(w, httpStatus, teamLeaveHTTPResponse{Code: code, Msg: message})
		return
	}
	if result == nil || result.GetOperationId() <= 0 || result.GetTeamId() != teamID || result.GetGeneration() <= 0 || result.GetStatus() < 0 || result.GetStatus() > 1 || requireCompleted && result.GetStatus() != 1 {
		httpx.WriteJson(w, http.StatusBadGateway, teamLeaveHTTPResponse{Code: errcode.ErrInternal, Msg: "invalid team leave response"})
		return
	}
	httpx.WriteJson(w, http.StatusOK, teamLeaveHTTPResponse{Code: errcode.Success, Msg: "success", Data: &teamLeaveHTTPData{
		OperationID: strconv.FormatInt(result.GetOperationId(), 10), TeamID: strconv.FormatInt(result.GetTeamId(), 10),
		Generation: strconv.FormatInt(result.GetGeneration(), 10), Status: result.GetStatus(),
	}})
}
