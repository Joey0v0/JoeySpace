package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/im/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type teamGroupUnreadResponse struct {
	Code int                  `json:"code"`
	Msg  string               `json:"msg"`
	Data *teamGroupUnreadData `json:"data,omitempty"`
}

type teamGroupUnreadData struct {
	TeamID      int64    `json:"team_id,string"`
	GroupID     int64    `json:"group_id,string"`
	UnreadCount int64    `json:"unread_count,string"`
	MessageIDs  []string `json:"message_ids,omitempty"`
}

func getTeamGroupUnreadHandler(client pb.IMClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, teamID, groupID, ok := teamGroupUnreadScope(w, r)
		if !ok {
			return
		}
		if client == nil {
			teamGroupUnreadRPCError(w, status.Error(codes.Unavailable, ""))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
		result, err := client.GetTeamGroupUnread(ctx, &pb.GetTeamGroupUnreadRequest{TeamId: teamID, GroupId: groupID})
		if err != nil {
			teamGroupUnreadRPCError(w, err)
			return
		}
		if result == nil || result.TeamId != teamID || result.GroupId != groupID || result.UnreadCount < 0 {
			writeTeamGroupUnreadError(w, http.StatusBadGateway, errcode.ErrInternal, "invalid IM service response")
			return
		}
		httpx.WriteJson(w, http.StatusOK, teamGroupUnreadResponse{Code: errcode.Success, Msg: "success", Data: &teamGroupUnreadData{
			TeamID: result.TeamId, GroupID: result.GroupId, UnreadCount: result.UnreadCount,
		}})
	}
}

func markTeamGroupMessagesReadHandler(client pb.IMClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, teamID, groupID, ok := teamGroupUnreadScope(w, r)
		if !ok {
			return
		}
		var body struct {
			MessageIDs []string `json:"message_ids"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			writeTeamGroupUnreadError(w, http.StatusBadRequest, errcode.ErrBadRequest, "invalid group read request")
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF || len(body.MessageIDs) == 0 || len(body.MessageIDs) > 100 {
			writeTeamGroupUnreadError(w, http.StatusBadRequest, errcode.ErrBadRequest, "invalid group read request")
			return
		}
		messageIDs := make([]int64, 0, len(body.MessageIDs))
		requested := make(map[int64]struct{}, len(body.MessageIDs))
		for _, rawID := range body.MessageIDs {
			id, valid := parseTaskNotificationDecimal(rawID)
			if !valid || id <= 0 {
				writeTeamGroupUnreadError(w, http.StatusBadRequest, errcode.ErrBadRequest, "invalid group read request")
				return
			}
			if _, exists := requested[id]; !exists {
				requested[id] = struct{}{}
				messageIDs = append(messageIDs, id)
			}
		}
		slices.Sort(messageIDs)
		if client == nil {
			teamGroupUnreadRPCError(w, status.Error(codes.Unavailable, ""))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
		result, err := client.MarkTeamGroupMessagesRead(ctx, &pb.MarkTeamGroupMessagesReadRequest{TeamId: teamID, GroupId: groupID, MessageIds: messageIDs})
		if err != nil {
			teamGroupUnreadRPCError(w, err)
			return
		}
		if result == nil || result.TeamId != teamID || result.GroupId != groupID || result.UnreadCount < 0 || len(result.MessageIds) != len(requested) {
			writeTeamGroupUnreadError(w, http.StatusBadGateway, errcode.ErrInternal, "invalid IM service response")
			return
		}
		// Remove each echo exactly once: extra, missing, and duplicate IDs all fail.
		for _, id := range result.MessageIds {
			if _, exists := requested[id]; !exists || id <= 0 {
				writeTeamGroupUnreadError(w, http.StatusBadGateway, errcode.ErrInternal, "invalid IM service response")
				return
			}
			delete(requested, id)
		}
		ids := make([]string, len(messageIDs))
		for i, id := range messageIDs {
			ids[i] = strconv.FormatInt(id, 10)
		}
		httpx.WriteJson(w, http.StatusOK, teamGroupUnreadResponse{Code: errcode.Success, Msg: "success", Data: &teamGroupUnreadData{
			TeamID: result.TeamId, GroupID: result.GroupId, UnreadCount: result.UnreadCount, MessageIDs: ids,
		}})
	}
}

func teamGroupUnreadScope(w http.ResponseWriter, r *http.Request) (string, int64, int64, bool) {
	token, ok := offlineBearerToken(r)
	if !ok {
		writeTeamGroupUnreadError(w, http.StatusUnauthorized, errcode.ErrUnAuth, "login required")
		return "", 0, 0, false
	}
	vars := pathvar.Vars(r)
	teamID, validTeam := parseTaskNotificationDecimal(vars["team_id"])
	groupID, validGroup := parseTaskNotificationDecimal(vars["group_id"])
	if !validTeam || !validGroup || teamID <= 0 || groupID <= 0 || r.URL.RawQuery != "" || r.URL.ForceQuery {
		writeTeamGroupUnreadError(w, http.StatusBadRequest, errcode.ErrBadRequest, "invalid group read request")
		return "", 0, 0, false
	}
	return token, teamID, groupID, true
}

func writeTeamGroupUnreadError(w http.ResponseWriter, httpStatus, code int, message string) {
	httpx.WriteJson(w, httpStatus, teamGroupUnreadResponse{Code: code, Msg: message})
}

func teamGroupUnreadRPCError(w http.ResponseWriter, err error) {
	httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "IM service error"
	switch status.Code(err) {
	case codes.InvalidArgument:
		httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid group read request"
	case codes.Unauthenticated:
		httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
	case codes.PermissionDenied:
		httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "group and team membership required"
	case codes.NotFound:
		httpStatus, code, message = http.StatusNotFound, errcode.ErrGroupNotFound, "team group or messages not found"
	case codes.Unavailable:
		httpStatus, message = http.StatusServiceUnavailable, "IM service unavailable"
	case codes.DeadlineExceeded:
		httpStatus, message = http.StatusGatewayTimeout, "IM service timeout"
	}
	writeTeamGroupUnreadError(w, httpStatus, code, message)
}
