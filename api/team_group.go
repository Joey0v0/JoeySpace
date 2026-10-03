package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/im/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type createTeamGroupResponse struct {
	Code int                  `json:"code"`
	Msg  string               `json:"msg"`
	Data *createTeamGroupData `json:"data,omitempty"`
}

type createTeamGroupData struct {
	GroupID int64 `json:"group_id,string"`
}

func createTeamGroupHandler(client pb.IMClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 {
			httpx.WriteJson(w, http.StatusUnauthorized, createTeamGroupResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			httpx.WriteJson(w, http.StatusUnauthorized, createTeamGroupResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		keys := r.Header.Values("Idempotency-Key")
		if len(keys) != 1 || !validIdempotencyKey(keys[0]) {
			httpx.WriteJson(w, http.StatusBadRequest, createTeamGroupResponse{Code: errcode.ErrBadRequest, Msg: "invalid Idempotency-Key"})
			return
		}
		teamID, err := strconv.ParseInt(pathvar.Vars(r)["team_id"], 10, 64)
		if err != nil || teamID <= 0 {
			httpx.WriteJson(w, http.StatusBadRequest, createTeamGroupResponse{Code: errcode.ErrBadRequest, Msg: "invalid team ID"})
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			httpx.WriteJson(w, http.StatusBadRequest, createTeamGroupResponse{Code: errcode.ErrBadRequest, Msg: "invalid group request"})
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			httpx.WriteJson(w, http.StatusBadRequest, createTeamGroupResponse{Code: errcode.ErrBadRequest, Msg: "invalid group request"})
			return
		}
		name := strings.TrimSpace(req.Name)
		if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 64 {
			httpx.WriteJson(w, http.StatusBadRequest, createTeamGroupResponse{Code: errcode.ErrBadRequest, Msg: "invalid group name"})
			return
		}

		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1], "idempotency-key", keys[0]))
		result, err := client.CreateTeamGroup(ctx, &pb.CreateTeamGroupRequest{TeamId: teamID, Name: name})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "IM service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid team or group name"
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
			case codes.PermissionDenied:
				httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "team owner required"
			case codes.AlreadyExists:
				httpStatus, code, message = http.StatusConflict, errcode.ErrGroupRequestConflict, "Idempotency-Key already used for another group request"
			case codes.Unavailable:
				httpStatus, message = http.StatusServiceUnavailable, "IM service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "IM service timeout"
			}
			httpx.WriteJson(w, httpStatus, createTeamGroupResponse{Code: code, Msg: message})
			return
		}
		httpx.WriteJson(w, http.StatusOK, createTeamGroupResponse{Code: errcode.Success, Msg: "success", Data: &createTeamGroupData{GroupID: result.GetGroupId()}})
	}
}

func validIdempotencyKey(key string) bool {
	if len(key) < 1 || len(key) > 64 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
