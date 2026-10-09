package main

import (
	"context"
	"net/http"
	"net/url"

	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type taskDetailGetter interface {
	GetTask(context.Context, *taskpb.GetTaskRequest, ...grpc.CallOption) (*taskpb.GetTaskResponse, error)
}

type taskDetailData struct {
	Task            enrichedTaskItem `json:"task"`
	CanUpdateStatus bool             `json:"can_update_status"`
}

func getTaskDetailHandler(client taskDetailGetter, names taskNamesClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, ok := taskReadAuth(w, r)
		if !ok {
			return
		}
		teamID, teamErr := taskReadDecimal(pathvar.Vars(r)["team_id"])
		taskID, taskErr := taskReadDecimal(pathvar.Vars(r)["task_id"])
		query, queryErr := url.ParseQuery(r.URL.RawQuery)
		if teamErr != nil || taskErr != nil || teamID <= 0 || taskID <= 0 || queryErr != nil || len(query) != 0 {
			writeTaskReadError(w, status.Error(codes.InvalidArgument, "invalid task detail request"))
			return
		}
		response, err := client.GetTask(ctx, &taskpb.GetTaskRequest{TeamId: teamID, TaskId: taskID})
		if err != nil {
			writeTaskReadError(w, err)
			return
		}
		if response == nil || !validTaskReadItems([]*taskpb.TaskItem{response.Task}) || response.Task.TeamId != teamID || response.Task.TaskId != taskID {
			writeTaskReadError(w, invalidTaskReadResponse())
			return
		}
		tasks, err := enrichTaskReadItems(ctx, names, []*taskpb.TaskItem{response.Task})
		if err != nil {
			writeTaskReadError(w, err)
			return
		}
		httpx.WriteJson(w, http.StatusOK, taskReadResponse{Code: 0, Msg: "success", Data: taskDetailData{Task: tasks[0], CanUpdateStatus: response.CanUpdateStatus}})
	}
}
