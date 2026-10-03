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

func setTeamMemberRoleHandler(client pb.UserClient) http.HandlerFunc {
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

		vars := pathvar.Vars(r)
		teamID, teamErr := strconv.ParseInt(vars["team_id"], 10, 64)
		userID, userErr := strconv.ParseInt(vars["user_id"], 10, 64)
		if teamErr != nil || userErr != nil || teamID <= 0 || userID <= 0 {
			httpx.WriteJson(w, http.StatusBadRequest, response{Code: errcode.ErrBadRequest, Msg: "invalid team or user ID"})
			return
		}
		var req struct {
			Role *int32 `json:"role"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			httpx.WriteJson(w, http.StatusBadRequest, response{Code: errcode.ErrBadRequest, Msg: "invalid role request"})
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF || req.Role == nil || *req.Role != 0 && *req.Role != 1 {
			httpx.WriteJson(w, http.StatusBadRequest, response{Code: errcode.ErrBadRequest, Msg: "invalid role request"})
			return
		}

		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1]))
		_, err := client.SetTeamMemberRole(ctx, &pb.SetTeamMemberRoleRequest{TeamId: teamID, UserId: userID, Role: *req.Role})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "team service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid role request"
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
			case codes.PermissionDenied:
				httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "team owner required"
			case codes.NotFound:
				httpStatus, code, message = http.StatusNotFound, errcode.ErrNotFound, "team member not found"
			case codes.FailedPrecondition:
				httpStatus, code, message = http.StatusConflict, errcode.ErrTeamRoleConflict, "team member role cannot be changed"
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
