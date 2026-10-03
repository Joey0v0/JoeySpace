package main

import (
	"context"
	"encoding/json"
	"io"
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

type agentDraftPreparer interface {
	PrepareTaskDraft(context.Context, *pb.PrepareTaskDraftRequest, ...grpc.CallOption) (*pb.PrepareTaskDraftResponse, error)
}

type agentDraftReader interface {
	GetTaskDraft(context.Context, *pb.GetTaskDraftRequest, ...grpc.CallOption) (*pb.GetTaskDraftResponse, error)
}

type agentDraftResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data *agentDraftData `json:"data,omitempty"`
}

type agentDraftData struct {
	RunID       int64           `json:"run_id,string"`
	TeamID      int64           `json:"team_id,string,omitempty"`
	GroupID     int64           `json:"group_id,string,omitempty"`
	Status      string          `json:"status,omitempty"`
	TaskID      int64           `json:"task_id,string"`
	ReplyStatus string          `json:"reply_status,omitempty"`
	ReplyMsgID  string          `json:"reply_msg_id,omitempty"`
	Draft       *agentDraftItem `json:"draft,omitempty"`
}

type agentDraftItem struct {
	Revision           int64  `json:"revision,string,omitempty"`
	Title              string `json:"title"`
	Description        string `json:"description"`
	AssigneeID         int64  `json:"assignee_id,string"`
	AssigneeName       string `json:"assignee_name"`
	AssigneeResolution string `json:"assignee_resolution"`
	DueAtUnixMs        int64  `json:"due_at_unix_ms"`
	SourceMessageID    int64  `json:"source_message_id,string"`
}

const maxDraftDeadlineUnixMs int64 = 253402300799999

func parseDraftDeadline(value json.RawMessage) (int64, bool) {
	var deadline int64
	if len(value) == 0 || strings.TrimSpace(string(value)) == "null" {
		return 0, false
	}
	err := json.Unmarshal(value, &deadline)
	return deadline, err == nil && deadline >= 0 && deadline <= maxDraftDeadlineUnixMs
}

func draftToken(r *http.Request) (string, bool) {
	headers := r.Header.Values("Authorization")
	if len(headers) != 1 {
		return "", false
	}
	parts := strings.Fields(headers[0])
	returnToken := len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && parts[1] != ""
	if !returnToken {
		return "", false
	}
	return "Bearer " + parts[1], true
}

func parseDraftRevision(value string) (int64, bool) {
	version, err := strconv.ParseInt(value, 10, 64)
	return version, err == nil && version > 0 && strconv.FormatInt(version, 10) == value
}

func parseDraftAssigneeID(value string) (int64, bool) {
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil && id >= 0 && strconv.FormatInt(id, 10) == value
}

func validDraftAssignee(draft *pb.TaskDraftItem) bool {
	if draft == nil || draft.GetAssigneeId() < 0 || !utf8.ValidString(draft.GetAssigneeName()) ||
		utf8.RuneCountInString(draft.GetAssigneeName()) > 64 || strings.TrimSpace(draft.GetAssigneeName()) != draft.GetAssigneeName() {
		return false
	}
	switch draft.GetAssigneeResolution() {
	case "": // Older responses do not carry name/resolution metadata.
		return draft.GetAssigneeName() == ""
	case "none":
		return draft.GetAssigneeName() == "" && draft.GetAssigneeId() == 0
	case "matched":
		return draft.GetAssigneeName() != "" && draft.GetAssigneeId() > 0
	case "not_found", "ambiguous", "truncated":
		return draft.GetAssigneeName() != "" && draft.GetAssigneeId() == 0
	case "selected":
		return draft.GetAssigneeId() > 0
	case "unassigned":
		return draft.GetAssigneeId() == 0
	default:
		return false
	}
}

func draftRPCError(w http.ResponseWriter, err error) {
	httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "AI draft service error"
	switch status.Code(err) {
	case codes.InvalidArgument:
		httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid draft request"
	case codes.Unauthenticated:
		httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
	case codes.PermissionDenied:
		httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "group and team membership required"
	case codes.NotFound:
		httpStatus, code, message = http.StatusNotFound, errcode.ErrNotFound, "draft or team group not found"
	case codes.AlreadyExists:
		httpStatus, code, message = http.StatusConflict, errcode.ErrTaskRequestConflict, "Idempotency-Key already used for another draft request"
	case codes.Unavailable, codes.FailedPrecondition:
		httpStatus, message = http.StatusServiceUnavailable, "AI draft service unavailable"
	case codes.DeadlineExceeded:
		httpStatus, message = http.StatusGatewayTimeout, "AI draft service timeout"
	}
	httpx.WriteJson(w, httpStatus, agentDraftResponse{Code: code, Msg: message})
}

func prepareTaskDraftHandler(client agentDraftPreparer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := draftToken(r)
		if !ok {
			httpx.WriteJson(w, http.StatusUnauthorized, agentDraftResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
			return
		}
		keys := r.Header.Values("Idempotency-Key")
		if len(keys) != 1 || !validIdempotencyKey(keys[0]) {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "invalid Idempotency-Key"})
			return
		}
		vars := pathvar.Vars(r)
		teamID, teamErr := strconv.ParseInt(vars["team_id"], 10, 64)
		groupID, groupErr := strconv.ParseInt(vars["group_id"], 10, 64)
		if teamErr != nil || groupErr != nil || teamID <= 0 || groupID <= 0 {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "invalid team or group ID"})
			return
		}
		var body struct {
			Instruction string `json:"instruction"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "invalid draft request"})
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "invalid draft request"})
			return
		}
		instruction := strings.TrimSpace(body.Instruction)
		if !utf8.ValidString(instruction) || instruction == "" || utf8.RuneCountInString(instruction) > 2000 {
			httpx.WriteJson(w, http.StatusBadRequest, agentDraftResponse{Code: errcode.ErrBadRequest, Msg: "instruction must contain 1 to 2000 characters"})
			return
		}
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", token, "idempotency-key", keys[0]))
		result, err := client.PrepareTaskDraft(ctx, &pb.PrepareTaskDraftRequest{TeamId: teamID, GroupId: groupID, Instruction: instruction})
		if err != nil {
			draftRPCError(w, err)
			return
		}
		if result == nil || result.GetRunId() <= 0 {
			httpx.WriteJson(w, http.StatusBadGateway, agentDraftResponse{Code: errcode.ErrInternal, Msg: "AI draft service returned no run"})
			return
		}
		httpx.WriteJson(w, http.StatusOK, agentDraftResponse{Code: errcode.Success, Msg: "success", Data: &agentDraftData{RunID: result.GetRunId()}})
	}
}

func getTaskDraftHandler(client agentDraftReader) http.HandlerFunc {
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
		ctx := metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", token))
		result, err := client.GetTaskDraft(ctx, &pb.GetTaskDraftRequest{RunId: runID})
		if err != nil {
			draftRPCError(w, err)
			return
		}
		writeTaskDraftResult(w, runID, result)
	}
}

func writeTaskDraftResult(w http.ResponseWriter, runID int64, result *pb.GetTaskDraftResponse) {
	validResult := result != nil && result.GetRunId() == runID && result.GetTeamId() > 0 && result.GetGroupId() > 0 && result.GetDraft() != nil && result.GetDraft().GetRevision() >= 0
	if validResult {
		switch result.GetStatus() {
		case "waiting_confirmation", "creating":
			validResult = result.GetTaskId() == 0
		case "succeeded":
			validResult = result.GetTaskId() > 0
		default:
			validResult = false
		}
	}
	if !validResult || !validDraftReplyResult(result) || !validDraftAssignee(result.GetDraft()) ||
		result.GetDraft().GetDueAtUnixMs() < 0 || result.GetDraft().GetDueAtUnixMs() > maxDraftDeadlineUnixMs {
		httpx.WriteJson(w, http.StatusBadGateway, agentDraftResponse{Code: errcode.ErrInternal, Msg: "AI draft service returned invalid draft"})
		return
	}
	draft := result.GetDraft()
	httpx.WriteJson(w, http.StatusOK, agentDraftResponse{Code: errcode.Success, Msg: "success", Data: &agentDraftData{
		RunID: result.GetRunId(), TeamID: result.GetTeamId(), GroupID: result.GetGroupId(), Status: result.GetStatus(),
		TaskID:      result.GetTaskId(),
		ReplyStatus: result.GetReplyStatus(), ReplyMsgID: result.GetReplyMsgId(),
		Draft: &agentDraftItem{Revision: draft.GetRevision(), Title: draft.GetTitle(), Description: draft.GetDescription(), AssigneeID: draft.GetAssigneeId(), AssigneeName: draft.GetAssigneeName(), AssigneeResolution: draft.GetAssigneeResolution(), DueAtUnixMs: draft.GetDueAtUnixMs(), SourceMessageID: draft.GetSourceMessageId()},
	}})
}

func validDraftReplyResult(result *pb.GetTaskDraftResponse) bool {
	switch result.GetReplyStatus() {
	case "", "disabled", "not_started":
		// Missing fields from an older Agent are not proof that posting is enabled.
		return result.GetReplyMsgId() == ""
	case "unknown":
		return result.GetStatus() == "succeeded" && result.GetReplyMsgId() == ""
	case "pending", "accepted":
		return result.GetStatus() == "succeeded" && result.GetReplyMsgId() == model.BotTaskMsgIDPrefix+strconv.FormatInt(result.GetRunId(), 10)
	default:
		return false
	}
}
