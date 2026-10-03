package main

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/agent/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type agentDraftAssigneeSelector interface {
	SelectTaskDraftAssignee(context.Context, *pb.SelectTaskDraftAssigneeRequest, ...grpc.CallOption) (*pb.GetTaskDraftResponse, error)
}

func selectTaskDraftAssigneeHandler(client agentDraftAssigneeSelector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := draftToken(r)
		if !ok {
			httpx.WriteJson(w, http.StatusUnauthorized, agentDraftResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		runID, validRunID := parseDraftRevision(pathvar.Vars(r)["run_id"])
		if !validRunID {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "invalid run ID"})
			return
		}
		var body struct {
			AssigneeID       string `json:"assignee_id"`
			ExpectedRevision string `json:"expected_revision"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "invalid assignee selection"})
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "invalid assignee selection"})
			return
		}
		assigneeID, validAssigneeID := parseDraftAssigneeID(body.AssigneeID)
		revision, validRevision := parseDraftRevision(body.ExpectedRevision)
		if !validAssigneeID || !validRevision {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "saved revision and explicit assignee ID are required"})
			return
		}
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", token))
		result, err := client.SelectTaskDraftAssignee(ctx, &pb.SelectTaskDraftAssigneeRequest{
			RunId: runID, AssigneeId: &assigneeID, ExpectedRevision: revision,
		})
		if err != nil {
			switch status.Code(err) {
			case codes.Aborted, codes.FailedPrecondition:
				httpx.WriteJson(w, http.StatusConflict, agentDraftResponse{Code: errcode.ErrAgentDraftConflict, Msg: "draft changed or cannot be edited; reload to review"})
			default:
				draftRPCError(w, err)
			}
			return
		}
		validRevisionResult := result != nil && (result.GetDraft().GetRevision() == revision ||
			(revision < math.MaxInt64 && result.GetDraft().GetRevision() == revision+1))
		if !validRevisionResult || result.GetStatus() != "waiting_confirmation" ||
			result.GetDraft().GetAssigneeId() != assigneeID ||
			(assigneeID > 0 && result.GetDraft().GetAssigneeResolution() != "selected") ||
			(assigneeID == 0 && result.GetDraft().GetAssigneeResolution() != "unassigned") {
			httpx.WriteJson(w, http.StatusBadGateway, agentDraftResponse{Code: errcode.ErrInternal, Msg: "assignee selection returned invalid saved draft; reload the same run"})
			return
		}
		writeTaskDraftResult(w, runID, result)
	}
}
