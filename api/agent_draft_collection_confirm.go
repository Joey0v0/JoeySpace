package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/agent/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type agentDraftItemConfirmer interface {
	ConfirmTaskDraftItem(context.Context, *pb.ConfirmTaskDraftItemRequest, ...grpc.CallOption) (*pb.GetTaskDraftItemResponse, error)
}

func confirmTaskDraftItemHandler(client agentDraftItemConfirmer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, index, ctx, ok := draftItemEditIdentity(w, r)
		if !ok {
			return
		}
		var body struct {
			ExpectedTitle              *string         `json:"expected_title"`
			ExpectedDescription        *string         `json:"expected_description"`
			ExpectedRevision           string          `json:"expected_revision"`
			ExpectedAssigneeID         string          `json:"expected_assignee_id"`
			ExpectedDueAtUnixMs        json.RawMessage `json:"expected_due_at_unix_ms"`
			ExpectedDeadlineResolution *string         `json:"expected_deadline_resolution"`
		}
		if !decodeDraftItemEditBody(w, r, &body, 32768) {
			return
		}
		revision, validRevision := parseDraftRevision(body.ExpectedRevision)
		assigneeID, validAssignee := parseDraftAssigneeID(body.ExpectedAssigneeID)
		deadline, validDeadline := parseDraftDeadline(body.ExpectedDueAtUnixMs)
		if body.ExpectedTitle == nil || body.ExpectedDescription == nil || body.ExpectedDeadlineResolution == nil || !validRevision || !validAssignee || !validDeadline {
			draftItemEditBadRequest(w)
			return
		}
		title, description, resolution := *body.ExpectedTitle, *body.ExpectedDescription, *body.ExpectedDeadlineResolution
		if !utf8.ValidString(title) || title != strings.TrimSpace(title) || utf8.RuneCountInString(title) < 1 || utf8.RuneCountInString(title) > 200 ||
			!utf8.ValidString(description) || description != strings.TrimSpace(description) || utf8.RuneCountInString(description) > 2000 || resolution == "" || !validDeadlineResolution(resolution) {
			draftItemEditBadRequest(w)
			return
		}
		result, err := client.ConfirmTaskDraftItem(ctx, &pb.ConfirmTaskDraftItemRequest{RunId: runID, ItemIndex: &index, ExpectedTitle: title, ExpectedDescription: description, ExpectedRevision: revision, ExpectedAssigneeId: &assigneeID, ExpectedDueAtUnixMs: &deadline, ExpectedDeadlineResolution: resolution})
		if err != nil {
			if status.Code(err) == codes.AlreadyExists {
				httpx.WriteJson(w, http.StatusConflict, agentDraftCollectionResponse{Code: errcode.ErrTaskRequestConflict, Msg: "task creation request conflict; reload this same item to check its state"})
			} else {
				draftCollectionRPCError(w, err)
			}
			return
		}
		if result == nil || !validDraftCollectionScope(runID, result.GetRunId(), result.GetTeamId(), result.GetGroupId(), result.GetItemCount()) || index >= result.GetItemCount() || !validDraftCollectionItem(result.GetItem(), index, runID) ||
			result.GetItem().GetStatus() != "succeeded" || result.GetItem().GetTaskId() <= 0 {
			invalidDraftCollectionResult(w)
			return
		}
		draft := result.GetItem().GetDraft()
		if draft.GetRevision() != revision || draft.GetTitle() != title || draft.GetDescription() != description || draft.GetAssigneeId() != assigneeID || draft.GetDueAtUnixMs() != deadline || draft.GetDeadline().GetResolution() != resolution {
			invalidDraftCollectionResult(w)
			return
		}
		httpx.WriteJson(w, http.StatusOK, agentDraftCollectionResponse{Code: errcode.Success, Msg: "success", Data: &agentDraftCollectionData{RunID: result.GetRunId(), TeamID: result.GetTeamId(), GroupID: result.GetGroupId(), ItemCount: result.GetItemCount(), Item: draftCollectionHTTPItem(result.GetItem())}})
	}
}
