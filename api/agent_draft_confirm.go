package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/agent/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type agentDraftConfirmer interface {
	ConfirmTaskDraft(context.Context, *pb.ConfirmTaskDraftRequest, ...grpc.CallOption) (*pb.GetTaskDraftResponse, error)
}

func confirmTaskDraftHandler(client agentDraftConfirmer) http.HandlerFunc {
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
			ExpectedDeadlineResolution json.RawMessage `json:"expected_deadline_resolution"`
			ExpectedDueAtUnixMs        json.RawMessage `json:"expected_due_at_unix_ms"`
			ExpectedAssigneeID         json.RawMessage `json:"expected_assignee_id"`
			ExpectedRevision           string          `json:"expected_revision"`
			ExpectedTitle              *string         `json:"expected_title"`
			ExpectedDescription        *string         `json:"expected_description"`
		}
		// Supplementary Unicode characters can need 12 bytes per escaped rune.
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "invalid draft confirmation"})
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF || body.ExpectedTitle == nil || body.ExpectedDescription == nil {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "both saved draft text fields are required"})
			return
		}
		if !utf8.ValidString(*body.ExpectedTitle) || strings.TrimSpace(*body.ExpectedTitle) == "" ||
			utf8.RuneCountInString(*body.ExpectedTitle) > 200 || !utf8.ValidString(*body.ExpectedDescription) ||
			utf8.RuneCountInString(*body.ExpectedDescription) > 2000 {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "invalid saved draft text"})
			return
		}
		revision, validRevision := parseDraftRevision(body.ExpectedRevision)
		if !validRevision {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "saved draft revision is required"})
			return
		}
		var expectedAssigneeID *int64
		if len(body.ExpectedAssigneeID) != 0 {
			var value string
			if err := json.Unmarshal(body.ExpectedAssigneeID, &value); err != nil {
				httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "invalid reviewed assignee ID"})
				return
			}
			id, validID := parseDraftAssigneeID(value)
			if !validID {
				httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "invalid reviewed assignee ID"})
				return
			}
			expectedAssigneeID = &id
		}
		var expectedDueAtUnixMs *int64
		if len(body.ExpectedDueAtUnixMs) != 0 {
			deadline, validDeadline := parseDraftDeadline(body.ExpectedDueAtUnixMs)
			if !validDeadline {
				httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "invalid reviewed deadline"})
				return
			}
			expectedDueAtUnixMs = &deadline
		}
		expectedDeadlineResolution := ""
		if len(body.ExpectedDeadlineResolution) != 0 {
			if strings.TrimSpace(string(body.ExpectedDeadlineResolution)) == "null" ||
				json.Unmarshal(body.ExpectedDeadlineResolution, &expectedDeadlineResolution) != nil || !validDeadlineResolution(expectedDeadlineResolution) {
				httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "invalid reviewed deadline resolution"})
				return
			}
		}
		// Task's stable operation key belongs to Agent, never HTTP headers/body.
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", token))
		result, err := client.ConfirmTaskDraft(ctx, &pb.ConfirmTaskDraftRequest{
			ExpectedDeadlineResolution: expectedDeadlineResolution,
			ExpectedDueAtUnixMs:        expectedDueAtUnixMs,
			ExpectedAssigneeId:         expectedAssigneeID,
			ExpectedRevision:           revision,
			RunId:                      runID, ExpectedTitle: *body.ExpectedTitle, ExpectedDescription: *body.ExpectedDescription,
		})
		if err != nil {
			switch status.Code(err) {
			case codes.Aborted, codes.FailedPrecondition:
				httpx.WriteJson(w, http.StatusConflict, agentDraftResponse{Code: errcode.ErrAgentDraftConflict, Msg: "draft changed or cannot be confirmed; reload to review"})
			case codes.AlreadyExists:
				httpx.WriteJson(w, http.StatusConflict, agentDraftResponse{Code: errcode.ErrTaskRequestConflict, Msg: "task creation request conflict; reload the same run to check its state"})
			default:
				draftRPCError(w, err)
			}
			return
		}
		if result == nil || result.GetStatus() != "succeeded" || result.GetTaskId() <= 0 || result.GetDraft().GetRevision() != revision ||
			(expectedAssigneeID != nil && result.GetDraft().GetAssigneeId() != *expectedAssigneeID) ||
			(expectedDueAtUnixMs != nil && result.GetDraft().GetDueAtUnixMs() != *expectedDueAtUnixMs) ||
			result.GetDraft().GetDeadline().GetResolution() != expectedDeadlineResolution {
			httpx.WriteJson(w, http.StatusBadGateway, agentDraftResponse{Code: errcode.ErrInternal, Msg: "confirmation returned no saved task result; reload the same run"})
			return
		}
		writeTaskDraftResult(w, runID, result)
	}
}
