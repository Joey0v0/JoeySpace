package main

import (
	"context"
	"net/http"
	"net/url"

	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type myTaskLister interface {
	ListMyTasks(context.Context, *taskpb.ListMyTasksRequest, ...grpc.CallOption) (*taskpb.ListMyTasksResponse, error)
}

type myTasksData struct {
	Tasks      []enrichedTaskItem `json:"tasks"`
	NextCursor string             `json:"next_cursor"`
}

func parseMyTasksQuery(raw string) (*taskpb.ListMyTasksRequest, error) {
	bad := status.Error(codes.InvalidArgument, "invalid task filters")
	query, err := url.ParseQuery(raw)
	if err != nil {
		return nil, bad
	}
	for key, values := range query {
		if len(values) != 1 {
			return nil, bad
		}
		switch key {
		case "view", "team_id", "cursor", "limit":
		default:
			return nil, bad
		}
	}
	req := &taskpb.ListMyTasksRequest{}
	switch query.Get("view") {
	case "open":
		req.View = 0
	case "completed":
		req.View = 1
	default:
		return nil, bad
	}
	if values, ok := query["team_id"]; ok {
		req.TeamId, err = taskReadDecimal(values[0])
		if err != nil {
			return nil, bad
		}
	}
	if values, ok := query["cursor"]; ok {
		if values[0] == "" || len(values[0]) > 2048 {
			return nil, bad
		}
		req.Cursor = values[0]
	}
	if values, ok := query["limit"]; ok {
		limit, err := taskReadDecimal(values[0])
		if err != nil || limit < 1 || limit > 50 {
			return nil, bad
		}
		req.Limit = int32(limit)
	}
	return req, nil
}

func listMyTasksHandler(client myTaskLister, names taskNamesClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, ok := taskReadAuth(w, r)
		if !ok {
			return
		}
		req, err := parseMyTasksQuery(r.URL.RawQuery)
		if err != nil {
			writeTaskReadError(w, err)
			return
		}
		response, err := client.ListMyTasks(ctx, req)
		if err != nil {
			writeTaskReadError(w, err)
			return
		}
		if response == nil || !validTaskReadItems(response.Tasks) {
			writeTaskReadError(w, invalidTaskReadResponse())
			return
		}
		tasks, err := enrichTaskReadItems(ctx, names, response.Tasks)
		if err != nil {
			writeTaskReadError(w, err)
			return
		}
		httpx.WriteJson(w, http.StatusOK, taskReadResponse{Code: 0, Msg: "success", Data: myTasksData{Tasks: tasks, NextCursor: response.NextCursor}})
	}
}
