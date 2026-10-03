package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
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

type taskStatusSetter interface {
	SetTaskStatus(context.Context, *pb.SetTaskStatusRequest, ...grpc.CallOption) (*pb.SetTaskStatusResponse, error)
}

type taskStatusResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func setTaskStatusHandler(client taskStatusSetter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 {
			httpx.WriteJson(w, http.StatusUnauthorized, taskStatusResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			httpx.WriteJson(w, http.StatusUnauthorized, taskStatusResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		vars := pathvar.Vars(r)
		teamID, teamErr := strconv.ParseInt(vars["team_id"], 10, 64)
		taskID, taskErr := strconv.ParseInt(vars["task_id"], 10, 64)
		if teamErr != nil || taskErr != nil || teamID <= 0 || taskID <= 0 {
			httpx.WriteJson(w, http.StatusBadRequest, taskStatusResponse{Code: errcode.ErrBadRequest, Msg: "invalid team or task ID"})
			return
		}
		var body struct {
			Status *int32 `json:"status"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			httpx.WriteJson(w, http.StatusBadRequest, taskStatusResponse{Code: errcode.ErrBadRequest, Msg: "invalid task status request"})
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF || body.Status == nil || *body.Status < 0 || *body.Status > 2 {
			httpx.WriteJson(w, http.StatusBadRequest, taskStatusResponse{Code: errcode.ErrBadRequest, Msg: "invalid task status request"})
			return
		}
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1]))
		_, err := client.SetTaskStatus(ctx, &pb.SetTaskStatusRequest{TeamId: teamID, TaskId: taskID, Status: *body.Status})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "task service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid task status request"
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
			case codes.PermissionDenied:
				httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "task status update not allowed"
			case codes.NotFound:
				httpStatus, code, message = http.StatusNotFound, errcode.ErrNotFound, "task not found"
			case codes.Unavailable:
				httpStatus, message = http.StatusServiceUnavailable, "task service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "task service timeout"
			}
			httpx.WriteJson(w, httpStatus, taskStatusResponse{Code: code, Msg: message})
			return
		}
		httpx.WriteJson(w, http.StatusOK, taskStatusResponse{Code: errcode.Success, Msg: "success"})
	}
}
