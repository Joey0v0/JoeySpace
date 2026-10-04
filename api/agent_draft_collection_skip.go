package main

import (
	"context"
	"net/http"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/agent/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"google.golang.org/grpc"
)

type agentDraftItemSkipper interface {
	SkipTaskDraftItem(context.Context, *pb.SkipTaskDraftItemRequest, ...grpc.CallOption) (*pb.GetTaskDraftItemResponse, error)
}

func skipTaskDraftItemHandler(client agentDraftItemSkipper) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, index, ctx, ok := draftItemEditIdentity(w, r)
		if !ok {
			return
		}
		var body struct {
			ExpectedRevision string `json:"expected_revision"`
		}
		if !decodeDraftItemEditBody(w, r, &body, 1024) {
			return
		}
		revision, validRevision := parseDraftRevision(body.ExpectedRevision)
		if !validRevision {
			draftItemEditBadRequest(w)
			return
		}
		result, err := client.SkipTaskDraftItem(ctx, &pb.SkipTaskDraftItemRequest{RunId: runID, ItemIndex: &index, ExpectedRevision: revision})
		if err != nil {
			draftCollectionRPCError(w, err)
			return
		}
		if result == nil || !validDraftCollectionScope(runID, result.GetRunId(), result.GetTeamId(), result.GetGroupId(), result.GetItemCount()) || index >= result.GetItemCount() || !validDraftCollectionItem(result.GetItem(), index, runID) ||
			result.GetItem().GetStatus() != "skipped" || result.GetItem().GetTaskId() != 0 || result.GetItem().GetReplyStatus() != "disabled" || result.GetItem().GetReplyMsgId() != "" || result.GetItem().GetDraft().GetRevision() != revision {
			invalidDraftCollectionResult(w)
			return
		}
		httpx.WriteJson(w, http.StatusOK, agentDraftCollectionResponse{Code: errcode.Success, Msg: "success", Data: &agentDraftCollectionData{RunID: result.GetRunId(), TeamID: result.GetTeamId(), GroupID: result.GetGroupId(), ItemCount: result.GetItemCount(), Item: draftCollectionHTTPItem(result.GetItem())}})
	}
}
