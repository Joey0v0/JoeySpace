package main

import (
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

type memberListResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data *memberListData `json:"data,omitempty"`
}

type memberListData struct {
	Members         []memberData `json:"members"`
	NextAfterUserID int64        `json:"next_after_user_id,string"`
}

type memberData struct {
	UserID   int64  `json:"user_id,string"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Role     int32  `json:"role"`
}

func listTeamMembersHandler(client pb.UserClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 {
			httpx.WriteJson(w, http.StatusUnauthorized, memberListResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			httpx.WriteJson(w, http.StatusUnauthorized, memberListResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}

		teamID, err := strconv.ParseInt(pathvar.Vars(r)["team_id"], 10, 64)
		if err != nil || teamID <= 0 {
			httpx.WriteJson(w, http.StatusBadRequest, memberListResponse{Code: errcode.ErrBadRequest, Msg: "invalid team ID"})
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) > 2 {
			httpx.WriteJson(w, http.StatusBadRequest, memberListResponse{Code: errcode.ErrBadRequest, Msg: "invalid pagination"})
			return
		}
		for key := range query {
			if key != "after_user_id" && key != "limit" || len(query[key]) != 1 {
				httpx.WriteJson(w, http.StatusBadRequest, memberListResponse{Code: errcode.ErrBadRequest, Msg: "invalid pagination"})
				return
			}
		}
		var afterUserID int64
		if values, ok := query["after_user_id"]; ok {
			afterUserID, err = strconv.ParseInt(values[0], 10, 64)
			if err != nil || afterUserID < 0 {
				httpx.WriteJson(w, http.StatusBadRequest, memberListResponse{Code: errcode.ErrBadRequest, Msg: "invalid pagination"})
				return
			}
		}
		var limit int64
		if values, ok := query["limit"]; ok {
			limit, err = strconv.ParseInt(values[0], 10, 32)
			if err != nil || limit < 1 || limit > 100 {
				httpx.WriteJson(w, http.StatusBadRequest, memberListResponse{Code: errcode.ErrBadRequest, Msg: "invalid pagination"})
				return
			}
		}

		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1]))
		result, err := client.ListTeamMembers(ctx, &pb.ListTeamMembersRequest{
			TeamId: teamID, AfterUserId: afterUserID, Limit: int32(limit),
		})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "team service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid member list request"
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
			case codes.PermissionDenied:
				httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "team membership required"
			case codes.NotFound:
				httpStatus, code, message = http.StatusNotFound, errcode.ErrUserNotFound, "user not found"
			case codes.Unavailable:
				httpStatus, message = http.StatusServiceUnavailable, "team service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "team service timeout"
			}
			httpx.WriteJson(w, httpStatus, memberListResponse{Code: code, Msg: message})
			return
		}
		data := &memberListData{Members: make([]memberData, 0, len(result.GetMembers())), NextAfterUserID: result.GetNextAfterUserId()}
		for _, member := range result.GetMembers() {
			data.Members = append(data.Members, memberData{
				UserID: member.GetUserId(), Username: member.GetUsername(), Nickname: member.GetNickname(), Role: member.GetRole(),
			})
		}
		httpx.WriteJson(w, http.StatusOK, memberListResponse{Code: errcode.Success, Msg: "success", Data: data})
	}
}
