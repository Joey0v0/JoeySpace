package main

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/agent/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type agentReplyRetrier interface {
	RetryTaskReply(context.Context, *pb.GetTaskDraftRequest, ...grpc.CallOption) (*pb.GetTaskDraftResponse, error)
}

func retryTaskReplyHandler(client agentReplyRetrier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := draftToken(r)
		if !ok {
			httpx.WriteJson(w, http.StatusUnauthorized, agentDraftResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		runID, err := strconv.ParseInt(pathvar.Vars(r)["run_id"], 10, 64)
		if err != nil || runID <= 0 {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "invalid run ID"})
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
		if err != nil || strings.TrimSpace(string(body)) != "" || r.URL.RawQuery != "" {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "reply retry accepts only run ID and login token; body and query must be empty"})
			return
		}
		// The Agent owns frozen content, scope, task ID and operation identity.
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", token))
		result, err := client.RetryTaskReply(ctx, &pb.GetTaskDraftRequest{RunId: runID})
		if err != nil {
			switch status.Code(err) {
			case codes.Aborted, codes.FailedPrecondition, codes.AlreadyExists:
				httpx.WriteJson(w, http.StatusConflict, agentDraftResponse{Code: errcode.ErrAgentDraftConflict, Msg: "reply cannot be retried or conflicts with saved result; reload this run"})
			default:
				draftRPCError(w, err)
			}
			return
		}
		if result == nil || result.GetStatus() != "succeeded" || result.GetTaskId() <= 0 || result.GetReplyStatus() != "accepted" {
			httpx.WriteJson(w, http.StatusBadGateway, agentDraftResponse{Code: errcode.ErrInternal, Msg: "reply acceptance not confirmed; reload this run before retrying"})
			return
		}
		writeTaskDraftResult(w, runID, result)
	}
}
