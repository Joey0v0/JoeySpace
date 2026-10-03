package main

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/task/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type teamTaskLister interface {
	ListTeamTasks(context.Context, *pb.ListTeamTasksRequest, ...grpc.CallOption) (*pb.ListTeamTasksResponse, error)
}

type taskListResponse struct {
	Code int           `json:"code"`
	Msg  string        `json:"msg"`
	Data *taskListData `json:"data,omitempty"`
}

type taskListData struct {
	Tasks           []taskListItem `json:"tasks"`
	NextAfterTaskID int64          `json:"next_after_task_id,string"`
}

type taskListItem struct {
	TaskID          int64  `json:"task_id,string"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	CreatorID       int64  `json:"creator_id,string"`
	AssigneeID      int64  `json:"assignee_id,string"`
	Status          int32  `json:"status"`
	SourceGroupID   int64  `json:"source_group_id,string"`
	SourceMessageID int64  `json:"source_message_id,string"`
	DueAtUnixMs     int64  `json:"due_at_unix_ms"`
}

func listTeamTasksHandler(client teamTaskLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 {
			httpx.WriteJson(w, http.StatusUnauthorized, taskListResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			httpx.WriteJson(w, http.StatusUnauthorized, taskListResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		teamID, err := strconv.ParseInt(pathvar.Vars(r)["team_id"], 10, 64)
		if err != nil || teamID <= 0 {
			httpx.WriteJson(w, http.StatusBadRequest, taskListResponse{Code: errcode.ErrBadRequest, Msg: "invalid team ID"})
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) > 2 {
			httpx.WriteJson(w, http.StatusBadRequest, taskListResponse{Code: errcode.ErrBadRequest, Msg: "invalid pagination"})
			return
		}
		for key := range query {
			if key != "after_task_id" && key != "limit" || len(query[key]) != 1 {
				httpx.WriteJson(w, http.StatusBadRequest, taskListResponse{Code: errcode.ErrBadRequest, Msg: "invalid pagination"})
				return
			}
		}
		var afterTaskID int64
		if values, ok := query["after_task_id"]; ok {
			afterTaskID, err = strconv.ParseInt(values[0], 10, 64)
			if err != nil || afterTaskID < 0 {
				httpx.WriteJson(w, http.StatusBadRequest, taskListResponse{Code: errcode.ErrBadRequest, Msg: "invalid pagination"})
				return
			}
		}
		var limit int64
		if values, ok := query["limit"]; ok {
			limit, err = strconv.ParseInt(values[0], 10, 32)
			if err != nil || limit < 1 || limit > 100 {
				httpx.WriteJson(w, http.StatusBadRequest, taskListResponse{Code: errcode.ErrBadRequest, Msg: "invalid pagination"})
				return
			}
		}
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1]))
		result, err := client.ListTeamTasks(ctx, &pb.ListTeamTasksRequest{TeamId: teamID, AfterTaskId: afterTaskID, Limit: int32(limit)})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "task service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid task list request"
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
			case codes.PermissionDenied:
				httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "team membership required"
			case codes.Unavailable:
				httpStatus, message = http.StatusServiceUnavailable, "task service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "task service timeout"
			}
			httpx.WriteJson(w, httpStatus, taskListResponse{Code: code, Msg: message})
			return
		}
		data := &taskListData{Tasks: make([]taskListItem, 0, len(result.GetTasks())), NextAfterTaskID: result.GetNextAfterTaskId()}
		for _, task := range result.GetTasks() {
			data.Tasks = append(data.Tasks, taskListItem{TaskID: task.GetTaskId(), Title: task.GetTitle(), Description: task.GetDescription(), CreatorID: task.GetCreatorId(), AssigneeID: task.GetAssigneeId(), Status: task.GetStatus(), SourceGroupID: task.GetSourceGroupId(), SourceMessageID: task.GetSourceMessageId(), DueAtUnixMs: task.GetDueAtUnixMs()})
		}
		httpx.WriteJson(w, http.StatusOK, taskListResponse{Code: errcode.Success, Msg: "success", Data: data})
	}
}
