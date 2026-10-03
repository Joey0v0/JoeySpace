package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/task/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type taskCreator interface {
	CreateTask(context.Context, *pb.CreateTaskRequest, ...grpc.CallOption) (*pb.CreateTaskResponse, error)
}

type createTaskResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data *createTaskData `json:"data,omitempty"`
}

type createTaskData struct {
	TaskID int64 `json:"task_id,string"`
}

func createTaskHandler(client taskCreator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 {
			httpx.WriteJson(w, http.StatusUnauthorized, createTaskResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			httpx.WriteJson(w, http.StatusUnauthorized, createTaskResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		keys := r.Header.Values("Idempotency-Key")
		if len(keys) != 1 || !validIdempotencyKey(keys[0]) {
			httpx.WriteJson(w, http.StatusBadRequest, createTaskResponse{Code: errcode.ErrBadRequest, Msg: "invalid Idempotency-Key"})
			return
		}
		teamID, err := strconv.ParseInt(pathvar.Vars(r)["team_id"], 10, 64)
		if err != nil || teamID <= 0 {
			httpx.WriteJson(w, http.StatusBadRequest, createTaskResponse{Code: errcode.ErrBadRequest, Msg: "invalid team ID"})
			return
		}
		var body struct {
			Title           string `json:"title"`
			Description     string `json:"description"`
			AssigneeID      int64  `json:"assignee_id,string"`
			SourceGroupID   int64  `json:"source_group_id,string"`
			SourceMessageID int64  `json:"source_message_id,string"`
			DueAtUnixMs     int64  `json:"due_at_unix_ms"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			httpx.WriteJson(w, http.StatusBadRequest, createTaskResponse{Code: errcode.ErrBadRequest, Msg: "invalid task request"})
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			httpx.WriteJson(w, http.StatusBadRequest, createTaskResponse{Code: errcode.ErrBadRequest, Msg: "invalid task request"})
			return
		}
		title, description := strings.TrimSpace(body.Title), strings.TrimSpace(body.Description)
		if !utf8.ValidString(title) || utf8.RuneCountInString(title) < 1 || utf8.RuneCountInString(title) > 200 || !utf8.ValidString(description) || utf8.RuneCountInString(description) > 2000 || body.AssigneeID < 0 || body.SourceGroupID < 0 || body.SourceMessageID < 0 || (body.SourceGroupID == 0) != (body.SourceMessageID == 0) || body.DueAtUnixMs < 0 || body.DueAtUnixMs > 253402300799999 {
			httpx.WriteJson(w, http.StatusBadRequest, createTaskResponse{Code: errcode.ErrBadRequest, Msg: "invalid task fields"})
			return
		}

		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1], "idempotency-key", keys[0]))
		result, err := client.CreateTask(ctx, &pb.CreateTaskRequest{TeamId: teamID, Title: title, Description: description, AssigneeId: body.AssigneeID, SourceGroupId: body.SourceGroupID, SourceMessageId: body.SourceMessageID, DueAtUnixMs: body.DueAtUnixMs})
		if err != nil {
			httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "task service error"
			switch status.Code(err) {
			case codes.InvalidArgument:
				httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid task fields"
			case codes.Unauthenticated:
				httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
			case codes.PermissionDenied:
				httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "team or source group membership required"
			case codes.NotFound:
				httpStatus, code, message = http.StatusNotFound, errcode.ErrNotFound, "assignee or source message not found"
			case codes.FailedPrecondition:
				httpStatus, code, message = http.StatusConflict, errcode.ErrUserBanned, "assignee account is disabled"
			case codes.AlreadyExists:
				httpStatus, code, message = http.StatusConflict, errcode.ErrTaskRequestConflict, "Idempotency-Key already used for another task request"
			case codes.Unavailable:
				httpStatus, message = http.StatusServiceUnavailable, "task service unavailable"
			case codes.DeadlineExceeded:
				httpStatus, message = http.StatusGatewayTimeout, "task service timeout"
			}
			httpx.WriteJson(w, httpStatus, createTaskResponse{Code: code, Msg: message})
			return
		}
		httpx.WriteJson(w, http.StatusOK, createTaskResponse{Code: errcode.Success, Msg: "success", Data: &createTaskData{TaskID: result.GetTaskId()}})
	}
}
