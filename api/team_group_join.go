package main

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/im/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type joinTeamGroupResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func joinTeamGroupHandler(client pb.IMClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 {
			httpx.WriteJson(w, http.StatusUnauthorized, joinTeamGroupResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			httpx.WriteJson(w, http.StatusUnauthorized, joinTeamGroupResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		vars := pathvar.Vars(r)
		teamID, teamErr := strconv.ParseInt(vars["team_id"], 10, 64)
		groupID, groupErr := strconv.ParseInt(vars["group_id"], 10, 64)
		if teamErr != nil || groupErr != nil || teamID <= 0 || groupID <= 0 {
			httpx.WriteJson(w, http.StatusBadRequest, joinTeamGroupResponse{Code: errcode.ErrBadRequest, Msg: "invalid team or group ID"})
			return
		}
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1]))
		_, err := client.JoinTeamGroup(ctx, &pb.JoinTeamGroupRequest{TeamId: teamID, GroupId: groupID})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "IM service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid team group request"
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
			case codes.PermissionDenied:
				httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "team membership required"
			case codes.NotFound:
				httpStatus, code, message = http.StatusNotFound, errcode.ErrGroupNotFound, "team group not found"
			case codes.Unavailable:
				httpStatus, message = http.StatusServiceUnavailable, "IM service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "IM service timeout"
			}
			httpx.WriteJson(w, httpStatus, joinTeamGroupResponse{Code: code, Msg: message})
			return
		}
		httpx.WriteJson(w, http.StatusOK, joinTeamGroupResponse{Code: errcode.Success, Msg: "success"})
	}
}
