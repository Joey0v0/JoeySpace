package main

import (
	"net/http"
	"net/url"
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

type teamGroupListResponse struct {
	Code int                `json:"code"`
	Msg  string             `json:"msg"`
	Data *teamGroupListData `json:"data,omitempty"`
}

type teamGroupListData struct {
	Groups           []teamGroupData `json:"groups"`
	NextAfterGroupID int64           `json:"next_after_group_id,string"`
}

type teamGroupData struct {
	GroupID int64  `json:"group_id,string"`
	Name    string `json:"name"`
	OwnerID int64  `json:"owner_id,string"`
	Joined  bool   `json:"joined"`
}

func listTeamGroupsHandler(client pb.IMClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 {
			httpx.WriteJson(w, http.StatusUnauthorized, teamGroupListResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			httpx.WriteJson(w, http.StatusUnauthorized, teamGroupListResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		teamID, err := strconv.ParseInt(pathvar.Vars(r)["team_id"], 10, 64)
		if err != nil || teamID <= 0 {
			httpx.WriteJson(w, http.StatusBadRequest, teamGroupListResponse{Code: errcode.ErrBadRequest, Msg: "invalid team ID"})
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) > 2 {
			httpx.WriteJson(w, http.StatusBadRequest, teamGroupListResponse{Code: errcode.ErrBadRequest, Msg: "invalid pagination"})
			return
		}
		for key := range query {
			if key != "after_group_id" && key != "limit" || len(query[key]) != 1 {
				httpx.WriteJson(w, http.StatusBadRequest, teamGroupListResponse{Code: errcode.ErrBadRequest, Msg: "invalid pagination"})
				return
			}
		}
		var afterGroupID int64
		if values, ok := query["after_group_id"]; ok {
			afterGroupID, err = strconv.ParseInt(values[0], 10, 64)
			if err != nil || afterGroupID < 0 {
				httpx.WriteJson(w, http.StatusBadRequest, teamGroupListResponse{Code: errcode.ErrBadRequest, Msg: "invalid pagination"})
				return
			}
		}
		var limit int64
		if values, ok := query["limit"]; ok {
			limit, err = strconv.ParseInt(values[0], 10, 32)
			if err != nil || limit < 1 || limit > 100 {
				httpx.WriteJson(w, http.StatusBadRequest, teamGroupListResponse{Code: errcode.ErrBadRequest, Msg: "invalid pagination"})
				return
			}
		}
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1]))
		result, err := client.ListTeamGroups(ctx, &pb.ListTeamGroupsRequest{TeamId: teamID, AfterGroupId: afterGroupID, Limit: int32(limit)})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "IM service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid team group list request"
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
			case codes.PermissionDenied:
				httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "team membership required"
			case codes.Unavailable:
				httpStatus, message = http.StatusServiceUnavailable, "IM service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "IM service timeout"
			}
			httpx.WriteJson(w, httpStatus, teamGroupListResponse{Code: code, Msg: message})
			return
		}
		data := &teamGroupListData{Groups: make([]teamGroupData, 0, len(result.GetGroups())), NextAfterGroupID: result.GetNextAfterGroupId()}
		for _, group := range result.GetGroups() {
			data.Groups = append(data.Groups, teamGroupData{GroupID: group.GetGroupId(), Name: group.GetName(), OwnerID: group.GetOwnerId(), Joined: group.GetJoined()})
		}
		httpx.WriteJson(w, http.StatusOK, teamGroupListResponse{Code: errcode.Success, Msg: "success", Data: data})
	}
}
