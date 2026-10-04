package main

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/agent/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type agentItemReplyRetrier interface {
	RetryTaskReplyItem(context.Context, *pb.GetTaskDraftItemRequest, ...grpc.CallOption) (*pb.GetTaskDraftItemResponse, error)
}

func retryTaskReplyItemHandler(client agentItemReplyRetrier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, index, ctx, ok := draftItemEditIdentity(w, r)
		if !ok {
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
		if err != nil || strings.TrimSpace(string(body)) != "" || r.URL.RawQuery != "" {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftCollectionResponse{Code: errcode.ErrBadRequest, Msg: "reply retry accepts only run ID, item index and login token; body and query must be empty"})
			return
		}
		// Frozen content and task identity remain owned by the Agent.
		result, err := client.RetryTaskReplyItem(ctx, &pb.GetTaskDraftItemRequest{RunId: runID, ItemIndex: &index})
		if err != nil {
			switch status.Code(err) {
			case codes.Aborted, codes.FailedPrecondition, codes.AlreadyExists:
				httpx.WriteJson(w, http.StatusConflict, agentDraftCollectionResponse{Code: errcode.ErrAgentDraftConflict, Msg: "reply cannot be retried or conflicts with saved result; reload this item"})
			default:
				draftRPCError(w, err)
			}
			return
		}
		if result == nil || !validDraftCollectionScope(runID, result.GetRunId(), result.GetTeamId(), result.GetGroupId(), result.GetItemCount()) || index >= result.GetItemCount() ||
			!validDraftCollectionItem(result.GetItem(), index, runID) || result.GetItem().GetStatus() != "succeeded" || result.GetItem().GetReplyStatus() != "accepted" {
			invalidDraftCollectionResult(w)
			return
		}
		httpx.WriteJson(w, http.StatusOK, agentDraftCollectionResponse{Code: errcode.Success, Msg: "success", Data: &agentDraftCollectionData{
			RunID: result.GetRunId(), TeamID: result.GetTeamId(), GroupID: result.GetGroupId(), ItemCount: result.GetItemCount(), Item: draftCollectionHTTPItem(result.GetItem()),
		}})
	}
}
