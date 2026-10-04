package main

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/agent/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type agentDraftItemTextEditor interface {
	EditTaskDraftItemText(context.Context, *pb.EditTaskDraftItemTextRequest, ...grpc.CallOption) (*pb.GetTaskDraftItemResponse, error)
}
type agentDraftItemAssigneeSelector interface {
	SelectTaskDraftItemAssignee(context.Context, *pb.SelectTaskDraftItemAssigneeRequest, ...grpc.CallOption) (*pb.GetTaskDraftItemResponse, error)
}
type agentDraftItemDeadlineEditor interface {
	EditTaskDraftItemDeadline(context.Context, *pb.EditTaskDraftItemDeadlineRequest, ...grpc.CallOption) (*pb.GetTaskDraftItemResponse, error)
}

func draftItemEditBadRequest(w http.ResponseWriter) {
	httpx.WriteJson(w, http.StatusBadRequest, agentDraftCollectionResponse{Code: errcode.ErrBadRequest, Msg: "invalid draft item edit request"})
}

func draftItemEditIdentity(w http.ResponseWriter, r *http.Request) (int64, int32, context.Context, bool) {
	token, ok := draftToken(r)
	if !ok {
		httpx.WriteJson(w, http.StatusUnauthorized, agentDraftCollectionResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
		return 0, 0, nil, false
	}
	vars := pathvar.Vars(r)
	runID, validRun := parseDraftRevision(vars["run_id"])
	index, err := strconv.ParseInt(vars["item_index"], 10, 32)
	if !validRun || err != nil || index < 0 || index > 4 || strconv.FormatInt(index, 10) != vars["item_index"] {
		draftItemEditBadRequest(w)
		return 0, 0, nil, false
	}
	return runID, int32(index), metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", token)), true
}

func decodeDraftItemEditBody(w http.ResponseWriter, r *http.Request, body any, maxBytes int64) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(body); err != nil {
		draftItemEditBadRequest(w)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		draftItemEditBadRequest(w)
		return false
	}
	return true
}

func writeDraftItemEditResult(w http.ResponseWriter, runID int64, index int32, revision int64, result *pb.GetTaskDraftItemResponse, matches func(*pb.TaskDraftItem) bool) {
	if result == nil || !validDraftCollectionScope(runID, result.GetRunId(), result.GetTeamId(), result.GetGroupId(), result.GetItemCount()) || index >= result.GetItemCount() || !validDraftCollectionItem(result.GetItem(), index, runID) ||
		result.GetItem().GetStatus() != "waiting_confirmation" || result.GetItem().GetTaskId() != 0 {
		invalidDraftCollectionResult(w)
		return
	}
	draft := result.GetItem().GetDraft()
	if !(draft.GetRevision() == revision || (revision < math.MaxInt64 && draft.GetRevision() == revision+1)) || !matches(draft) {
		invalidDraftCollectionResult(w)
		return
	}
	httpx.WriteJson(w, http.StatusOK, agentDraftCollectionResponse{Code: errcode.Success, Msg: "success", Data: &agentDraftCollectionData{RunID: result.GetRunId(), TeamID: result.GetTeamId(), GroupID: result.GetGroupId(), ItemCount: result.GetItemCount(), Item: draftCollectionHTTPItem(result.GetItem())}})
}

func editTaskDraftItemTextHandler(client agentDraftItemTextEditor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, index, ctx, ok := draftItemEditIdentity(w, r)
		if !ok {
			return
		}
		var body struct {
			Title            *string `json:"title"`
			Description      *string `json:"description"`
			ExpectedRevision string  `json:"expected_revision"`
		}
		if !decodeDraftItemEditBody(w, r, &body, 32768) {
			return
		}
		revision, validRevision := parseDraftRevision(body.ExpectedRevision)
		if body.Title == nil || body.Description == nil || !validRevision {
			draftItemEditBadRequest(w)
			return
		}
		title, description := strings.TrimSpace(*body.Title), strings.TrimSpace(*body.Description)
		if !utf8.ValidString(title) || utf8.RuneCountInString(title) < 1 || utf8.RuneCountInString(title) > 200 || !utf8.ValidString(description) || utf8.RuneCountInString(description) > 2000 {
			draftItemEditBadRequest(w)
			return
		}
		result, err := client.EditTaskDraftItemText(ctx, &pb.EditTaskDraftItemTextRequest{RunId: runID, ItemIndex: &index, Title: title, Description: description, ExpectedRevision: revision})
		if err != nil {
			draftCollectionRPCError(w, err)
			return
		}
		writeDraftItemEditResult(w, runID, index, revision, result, func(draft *pb.TaskDraftItem) bool {
			return draft.GetTitle() == title && draft.GetDescription() == description
		})
	}
}

func selectTaskDraftItemAssigneeHandler(client agentDraftItemAssigneeSelector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, index, ctx, ok := draftItemEditIdentity(w, r)
		if !ok {
			return
		}
		var body struct {
			AssigneeID       string `json:"assignee_id"`
			ExpectedRevision string `json:"expected_revision"`
		}
		if !decodeDraftItemEditBody(w, r, &body, 1024) {
			return
		}
		id, validID := parseDraftAssigneeID(body.AssigneeID)
		revision, validRevision := parseDraftRevision(body.ExpectedRevision)
		if !validID || !validRevision {
			draftItemEditBadRequest(w)
			return
		}
		result, err := client.SelectTaskDraftItemAssignee(ctx, &pb.SelectTaskDraftItemAssigneeRequest{RunId: runID, ItemIndex: &index, AssigneeId: &id, ExpectedRevision: revision})
		if err != nil {
			draftCollectionRPCError(w, err)
			return
		}
		writeDraftItemEditResult(w, runID, index, revision, result, func(draft *pb.TaskDraftItem) bool {
			return draft.GetAssigneeId() == id && ((id > 0 && draft.GetAssigneeResolution() == "selected") || (id == 0 && draft.GetAssigneeResolution() == "unassigned"))
		})
	}
}

func editTaskDraftItemDeadlineHandler(client agentDraftItemDeadlineEditor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, index, ctx, ok := draftItemEditIdentity(w, r)
		if !ok {
			return
		}
		var body struct {
			DueAtUnixMs      json.RawMessage `json:"due_at_unix_ms"`
			ExpectedRevision string          `json:"expected_revision"`
		}
		if !decodeDraftItemEditBody(w, r, &body, 1024) {
			return
		}
		deadline, validDeadline := parseDraftDeadline(body.DueAtUnixMs)
		revision, validRevision := parseDraftRevision(body.ExpectedRevision)
		if !validDeadline || !validRevision {
			draftItemEditBadRequest(w)
			return
		}
		result, err := client.EditTaskDraftItemDeadline(ctx, &pb.EditTaskDraftItemDeadlineRequest{RunId: runID, ItemIndex: &index, DueAtUnixMs: &deadline, ExpectedRevision: revision})
		if err != nil {
			draftCollectionRPCError(w, err)
			return
		}
		writeDraftItemEditResult(w, runID, index, revision, result, func(draft *pb.TaskDraftItem) bool {
			return draft.GetDueAtUnixMs() == deadline && ((deadline > 0 && draft.GetDeadline().GetResolution() == "selected") || (deadline == 0 && draft.GetDeadline().GetResolution() == "unset"))
		})
	}
}
