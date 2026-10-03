package main

import (
	"encoding/json"
	"io"
	"net/http"
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

func addTeamMemberHandler(client pb.UserClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 {
			httpx.WriteJson(w, http.StatusUnauthorized, response{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			httpx.WriteJson(w, http.StatusUnauthorized, response{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}

		teamID, err := strconv.ParseInt(pathvar.Vars(r)["team_id"], 10, 64)
		if err != nil || teamID <= 0 {
			httpx.WriteJson(w, http.StatusBadRequest, response{Code: errcode.ErrBadRequest, Msg: "invalid team ID"})
			return
		}
		var req struct {
			UserID int64 `json:"user_id,string"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			httpx.WriteJson(w, http.StatusBadRequest, response{Code: errcode.ErrBadRequest, Msg: "invalid member request"})
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF || req.UserID <= 0 {
			httpx.WriteJson(w, http.StatusBadRequest, response{Code: errcode.ErrBadRequest, Msg: "invalid member request"})
			return
		}

		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1]))
		_, err = client.AddTeamMember(ctx, &pb.AddTeamMemberRequest{TeamId: teamID, UserId: req.UserID})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "team service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid team or user ID"
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
			case codes.PermissionDenied:
				httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "team owner required"
			case codes.NotFound:
				httpStatus, code, message = http.StatusNotFound, errcode.ErrUserNotFound, "user not found"
			case codes.FailedPrecondition:
				httpStatus, code, message = http.StatusConflict, errcode.ErrUserBanned, "user is disabled"
			case codes.AlreadyExists:
				httpStatus, code, message = http.StatusConflict, errcode.ErrTeamMemberExist, "user is already a team member"
			case codes.Unavailable:
				httpStatus, message = http.StatusServiceUnavailable, "team service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "team service timeout"
			}
			httpx.WriteJson(w, httpStatus, response{Code: code, Msg: message})
			return
		}
		httpx.WriteJson(w, http.StatusOK, response{Code: errcode.Success, Msg: "success"})
	}
}
