package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/agent/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type agentDraftCollectionPreparer interface {
	PrepareTaskDraftCollection(context.Context, *pb.PrepareTaskDraftRequest, ...grpc.CallOption) (*pb.PrepareTaskDraftResponse, error)
}

type agentDraftCollectionReader interface {
	GetTaskDraftCollection(context.Context, *pb.GetTaskDraftRequest, ...grpc.CallOption) (*pb.GetTaskDraftCollectionResponse, error)
}

type agentDraftItemReader interface {
	GetTaskDraftItem(context.Context, *pb.GetTaskDraftItemRequest, ...grpc.CallOption) (*pb.GetTaskDraftItemResponse, error)
}

type collectionPreparationAdapter struct{ client agentDraftCollectionPreparer }

func (a collectionPreparationAdapter) PrepareTaskDraft(ctx context.Context, req *pb.PrepareTaskDraftRequest, opts ...grpc.CallOption) (*pb.PrepareTaskDraftResponse, error) {
	return a.client.PrepareTaskDraftCollection(ctx, req, opts...)
}

func prepareTaskDraftCollectionHandler(client agentDraftCollectionPreparer) http.HandlerFunc {
	return prepareTaskDraftHandlerWithError(collectionPreparationAdapter{client: client}, draftCollectionRPCError)
}

type agentDraftCollectionResponse struct {
	Code int                       `json:"code"`
	Msg  string                    `json:"msg"`
	Data *agentDraftCollectionData `json:"data,omitempty"`
}

type agentDraftCollectionData struct {
	RunID     int64                       `json:"run_id,string"`
	TeamID    int64                       `json:"team_id,string"`
	GroupID   int64                       `json:"group_id,string"`
	ItemCount int32                       `json:"item_count"`
	Items     []*agentDraftCollectionItem `json:"items,omitempty"`
	Item      *agentDraftCollectionItem   `json:"item,omitempty"`
}

type agentDraftCollectionItem struct {
	ItemIndex   int32           `json:"item_index"`
	Status      string          `json:"status"`
	Draft       *agentDraftItem `json:"draft"`
	TaskID      int64           `json:"task_id,string"`
	ReplyStatus string          `json:"reply_status"`
	ReplyMsgID  string          `json:"reply_msg_id"`
}

func draftCollectionRPCError(w http.ResponseWriter, err error) {
	if status.Code(err) == codes.FailedPrecondition || status.Code(err) == codes.Aborted {
		httpx.WriteJson(w, http.StatusConflict, agentDraftCollectionResponse{Code: errcode.ErrAgentDraftConflict, Msg: "draft request does not match this collection"})
		return
	}
	draftRPCError(w, err)
}

func invalidDraftCollectionResult(w http.ResponseWriter) {
	httpx.WriteJson(w, http.StatusBadGateway, agentDraftCollectionResponse{Code: errcode.ErrInternal, Msg: "AI draft service returned invalid collection"})
}

func validDraftCollectionScope(runID, resultRunID, teamID, groupID int64, count int32) bool {
	return runID == resultRunID && teamID > 0 && groupID > 0 && count >= 1 && count <= 5
}

func validDraftCollectionItem(item *pb.TaskDraftCollectionItem, index int32, runID int64) bool {
	expectedMsgID, err := model.BotTaskItemMsgID(runID, index)
	if err != nil || item == nil || item.ItemIndex == nil || item.GetItemIndex() != index {
		return false
	}
	switch item.GetStatus() {
	case "waiting_confirmation", "creating", "skipped":
		if item.GetTaskId() != 0 {
			return false
		}
		if item.GetStatus() == "skipped" && item.GetReplyStatus() != "disabled" {
			return false
		}
	case "succeeded":
		if item.GetTaskId() <= 0 {
			return false
		}
	default:
		return false
	}
	switch item.GetReplyStatus() {
	case "disabled", "not_started":
		if item.GetReplyMsgId() != "" {
			return false
		}
	case "pending", "accepted":
		if item.GetStatus() != "succeeded" || item.GetReplyMsgId() != expectedMsgID {
			return false
		}
	case "unknown":
		if item.GetStatus() != "succeeded" || item.GetReplyMsgId() != "" {
			return false
		}
	default:
		return false
	}
	draft := item.GetDraft()
	if draft == nil || draft.GetRevision() <= 0 || draft.GetSourceMessageId() < 0 || draft.GetAssigneeResolution() == "" || draft.GetDeadline() == nil ||
		!validDraftAssignee(draft) || !validDraftDeadlineMetadata(draft) {
		return false
	}
	if item.GetStatus() == "creating" || item.GetStatus() == "succeeded" {
		switch draft.GetAssigneeResolution() {
		case "not_found", "ambiguous", "truncated":
			return false
		}
		if draft.GetDeadline().GetResolution() == "needs_input" {
			return false
		}
	}
	return utf8.ValidString(draft.GetTitle()) && strings.TrimSpace(draft.GetTitle()) == draft.GetTitle() &&
		utf8.RuneCountInString(draft.GetTitle()) >= 1 && utf8.RuneCountInString(draft.GetTitle()) <= 200 &&
		utf8.ValidString(draft.GetDescription()) && strings.TrimSpace(draft.GetDescription()) == draft.GetDescription() && utf8.RuneCountInString(draft.GetDescription()) <= 2000
}

func draftCollectionHTTPItem(item *pb.TaskDraftCollectionItem) *agentDraftCollectionItem {
	return &agentDraftCollectionItem{ItemIndex: item.GetItemIndex(), Status: item.GetStatus(), Draft: draftHTTPItem(item.GetDraft()), TaskID: item.GetTaskId(), ReplyStatus: item.GetReplyStatus(), ReplyMsgID: item.GetReplyMsgId()}
}

func getTaskDraftCollectionHandler(client agentDraftCollectionReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := draftToken(r)
		if !ok {
			httpx.WriteJson(w, http.StatusUnauthorized, agentDraftCollectionResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		runID, ok := parseDraftRevision(pathvar.Vars(r)["run_id"])
		if !ok {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftCollectionResponse{Code: errcode.ErrBadRequest, Msg: "invalid run ID"})
			return
		}
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", token))
		result, err := client.GetTaskDraftCollection(ctx, &pb.GetTaskDraftRequest{RunId: runID})
		if err != nil {
			draftCollectionRPCError(w, err)
			return
		}
		if result == nil || !validDraftCollectionScope(runID, result.GetRunId(), result.GetTeamId(), result.GetGroupId(), result.GetItemCount()) || len(result.GetItems()) != int(result.GetItemCount()) {
			invalidDraftCollectionResult(w)
			return
		}
		items := make([]*agentDraftCollectionItem, 0, len(result.GetItems()))
		for i, item := range result.GetItems() {
			if !validDraftCollectionItem(item, int32(i), runID) {
				invalidDraftCollectionResult(w)
				return
			}
			items = append(items, draftCollectionHTTPItem(item))
		}
		httpx.WriteJson(w, http.StatusOK, agentDraftCollectionResponse{Code: errcode.Success, Msg: "success", Data: &agentDraftCollectionData{RunID: result.GetRunId(), TeamID: result.GetTeamId(), GroupID: result.GetGroupId(), ItemCount: result.GetItemCount(), Items: items}})
	}
}

func getTaskDraftItemHandler(client agentDraftItemReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := draftToken(r)
		if !ok {
			httpx.WriteJson(w, http.StatusUnauthorized, agentDraftCollectionResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		vars := pathvar.Vars(r)
		runID, validRun := parseDraftRevision(vars["run_id"])
		index, indexErr := strconv.ParseInt(vars["item_index"], 10, 32)
		if !validRun || indexErr != nil || index < 0 || index > 4 || strconv.FormatInt(index, 10) != vars["item_index"] {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftCollectionResponse{Code: errcode.ErrBadRequest, Msg: "invalid run ID or item index"})
			return
		}
		itemIndex := int32(index)
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", token))
		result, err := client.GetTaskDraftItem(ctx, &pb.GetTaskDraftItemRequest{RunId: runID, ItemIndex: &itemIndex})
		if err != nil {
			draftCollectionRPCError(w, err)
			return
		}
		if result == nil || !validDraftCollectionScope(runID, result.GetRunId(), result.GetTeamId(), result.GetGroupId(), result.GetItemCount()) || itemIndex >= result.GetItemCount() || !validDraftCollectionItem(result.GetItem(), itemIndex, runID) {
			invalidDraftCollectionResult(w)
			return
		}
		httpx.WriteJson(w, http.StatusOK, agentDraftCollectionResponse{Code: errcode.Success, Msg: "success", Data: &agentDraftCollectionData{RunID: result.GetRunId(), TeamID: result.GetTeamId(), GroupID: result.GetGroupId(), ItemCount: result.GetItemCount(), Item: draftCollectionHTTPItem(result.GetItem())}})
	}
}
