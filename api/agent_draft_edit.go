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
	"github.com/yjydist/go-im/rpc/agent/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type agentDraftEditor interface {
	EditTaskDraft(context.Context, *pb.EditTaskDraftRequest, ...grpc.CallOption) (*pb.GetTaskDraftResponse, error)
}

func editTaskDraftHandler(client agentDraftEditor) http.HandlerFunc {
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
		var body struct {
			ExpectedRevision    string  `json:"expected_revision"`
			Title               *string `json:"title"`
			Description         *string `json:"description"`
			ExpectedTitle       *string `json:"expected_title"`
			ExpectedDescription *string `json:"expected_description"`
		}
		// Four bounded text fields can reach 26 KB when Unicode is JSON-escaped.
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "invalid draft edit request"})
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF || body.Title == nil || body.Description == nil || body.ExpectedTitle == nil || body.ExpectedDescription == nil {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "all four draft text fields are required"})
			return
		}
		revision, validRevision := parseDraftRevision(body.ExpectedRevision)
		if !validRevision {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "saved draft revision is required"})
			return
		}
		title, description := strings.TrimSpace(*body.Title), strings.TrimSpace(*body.Description)
		validText := func(value string, limit int, required bool) bool {
			return utf8.ValidString(value) && utf8.RuneCountInString(value) <= limit && (!required || strings.TrimSpace(value) != "")
		}
		if !validText(title, 200, true) || !validText(description, 2000, false) ||
			!validText(*body.ExpectedTitle, 200, true) || !validText(*body.ExpectedDescription, 2000, false) {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "invalid draft text fields"})
			return
		}
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", token))
		result, err := client.EditTaskDraft(ctx, &pb.EditTaskDraftRequest{
			ExpectedRevision: revision,
			RunId:            runID, Title: title, Description: description,
			ExpectedTitle: *body.ExpectedTitle, ExpectedDescription: *body.ExpectedDescription,
		})
		if err != nil {
			switch status.Code(err) {
			case codes.Aborted:
				httpx.WriteJson(w, http.StatusConflict, agentDraftResponse{Code: errcode.ErrAgentDraftConflict, Msg: "draft changed; reload before editing"})
			case codes.FailedPrecondition:
				httpx.WriteJson(w, http.StatusConflict, agentDraftResponse{Code: errcode.ErrAgentDraftConflict, Msg: "draft is not awaiting confirmation"})
			default:
				draftRPCError(w, err)
			}
			return
		}
		if result == nil || result.GetDraft().GetRevision() <= 0 {
			httpx.WriteJson(w, http.StatusBadGateway, agentDraftResponse{Code: errcode.ErrInternal, Msg: "draft edit returned no saved revision"})
			return
		}
		writeTaskDraftResult(w, runID, result)
	}
}
