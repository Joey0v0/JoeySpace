package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/bwmarrin/snowflake"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type draftMessageReader interface {
	GroupMessages(context.Context, string, int64, int64) ([]*impb.TeamGroupMessage, error)
}

type taskDraftGenerator interface {
	GenerateDraft(context.Context, string, []*impb.TeamGroupMessage) (taskDraft, error)
}

type draftPreparationStore interface {
	findExistingDraft(context.Context, draftRunScope, string, string) (int64, error)
	saveWaitingDraft(context.Context, int64, draftRunScope, taskDraft, string, string) (int64, error)
}

type draftPreparer struct {
	identity  *draftIdentityResolver
	messages  draftMessageReader
	generator taskDraftGenerator
	store     draftPreparationStore
	idNode    *snowflake.Node
	assignees *draftAssigneeResolver
}

// prepare creates one draft after resolving the current user and reading the
// currently authorized group context. A model can suggest content, not scope.
func (p *draftPreparer) prepare(ctx context.Context, token string, teamID, groupID int64, instruction, requestKey string) (int64, error) {
	return p.prepareWithReference(ctx, token, teamID, groupID, instruction, requestKey, nil)
}

// The reference is a client-supplied interpretation input, not authorization or
// the server creation time. Missing values retain the original request identity.
func (p *draftPreparer) prepareWithReference(ctx context.Context, token string, teamID, groupID int64, instruction, requestKey string, reference *int64) (int64, error) {
	if p == nil || p.identity == nil || p.messages == nil || p.generator == nil || p.store == nil || p.idNode == nil {
		return 0, status.Error(codes.Unavailable, "draft preparation is not configured")
	}
	instruction = strings.TrimSpace(instruction)
	if teamID <= 0 || groupID <= 0 || !utf8.ValidString(instruction) || utf8.RuneCountInString(instruction) < 1 || utf8.RuneCountInString(instruction) > 2000 || !validDraftRequestKey(requestKey) ||
		(reference != nil && (*reference <= 0 || *reference > maxDraftDueAtUnixMs)) {
		return 0, status.Error(codes.InvalidArgument, "invalid draft request")
	}
	actorID, err := p.identity.currentUserID(ctx, token)
	if err != nil {
		return 0, err
	}
	messages, err := p.messages.GroupMessages(ctx, token, teamID, groupID)
	if err != nil {
		return 0, err
	}
	if ctx.Err() != nil {
		return 0, status.FromContextError(ctx.Err()).Err()
	}
	scope := draftRunScope{TeamID: teamID, GroupID: groupID, InitiatorID: actorID}
	fingerprint := draftPreparationFingerprintWithReference(teamID, groupID, instruction, reference)
	if existingID, err := p.store.findExistingDraft(ctx, scope, requestKey, fingerprint); err != nil || existingID > 0 {
		return existingID, err
	}
	draft, err := p.generator.GenerateDraft(ctx, instruction, messages)
	if ctx.Err() != nil {
		return 0, status.FromContextError(ctx.Err()).Err()
	}
	if err != nil {
		if status.Code(err) == codes.FailedPrecondition {
			return 0, status.Error(codes.FailedPrecondition, "model returned invalid draft")
		}
		return 0, status.Error(codes.Unavailable, "draft generator unavailable")
	}
	draft, err = p.verifyGeneratedDraft(ctx, token, scope, instruction, messages, draft, reference)
	if err != nil {
		return 0, err
	}
	runID := p.idNode.Generate().Int64()
	return p.store.saveWaitingDraft(ctx, runID, scope, draft, requestKey, fingerprint)
}

// verifyGeneratedDrafts checks candidates against already authorized context.
// Callers must first resolve the actor and current group scope. This helper is
// not an authorization entry point and never saves, creates or posts anything.
func (p *draftPreparer) verifyGeneratedDrafts(ctx context.Context, token string, scope draftRunScope, instruction string, messages []*impb.TeamGroupMessage, drafts []taskDraft, reference *int64) ([]taskDraft, error) {
	if len(drafts) < 1 || len(drafts) > maxGeneratedTaskDrafts {
		return nil, status.Error(codes.FailedPrecondition, "invalid draft collection size")
	}
	verified := make([]taskDraft, 0, len(drafts))
	for _, candidate := range drafts {
		draft, err := p.verifyGeneratedDraft(ctx, token, scope, instruction, messages, candidate, reference)
		if err != nil {
			return nil, err
		}
		verified = append(verified, draft)
	}
	return verified, nil
}

// Shared by the single saved-draft flow and internal batch candidate checking.
func (p *draftPreparer) verifyGeneratedDraft(ctx context.Context, token string, scope draftRunScope, instruction string, messages []*impb.TeamGroupMessage, draft taskDraft, reference *int64) (taskDraft, error) {
	if ctx.Err() != nil {
		return taskDraft{}, status.FromContextError(ctx.Err()).Err()
	}
	if p == nil {
		return taskDraft{}, status.Error(codes.Unavailable, "draft preparation is not configured")
	}
	draft, err := verifyGeneratedDraftEvidence(ctx, scope, instruction, messages, draft, reference)
	if err != nil {
		return taskDraft{}, err
	}
	draft, err = p.assignees.resolve(ctx, token, scope.TeamID, draft)
	if err != nil {
		return taskDraft{}, err
	}
	if _, err := newWaitingTaskDraftRun(scope, draft); err != nil {
		return taskDraft{}, status.Error(codes.FailedPrecondition, "generated draft is invalid")
	}
	return draft, nil
}

// The evidence boundary is shared by caller-authorized preparation and the
// saved-trigger flow. It neither authorizes scope nor resolves user identities.
func verifyGeneratedDraftEvidence(ctx context.Context, scope draftRunScope, instruction string, messages []*impb.TeamGroupMessage, draft taskDraft, reference *int64) (taskDraft, error) {
	if ctx.Err() != nil {
		return taskDraft{}, status.FromContextError(ctx.Err()).Err()
	}
	// The model supplies a literal mention, never a trusted member ID or state.
	if draft.AssigneeID != 0 || draft.AssigneeResolution != "" || draft.DueAtUnixMs != 0 || !sourceInAuthorizedText(messages, draft.SourceMessageID) {
		return taskDraft{}, status.Error(codes.FailedPrecondition, "draft source or fields could not be verified")
	}
	content := draft
	content.AssigneeName = ""
	content.Deadline = draftDeadlineMetadata{}
	if _, err := newWaitingTaskDraftRun(scope, content); err != nil {
		return taskDraft{}, status.Error(codes.FailedPrecondition, "generated draft is invalid")
	}
	evidence := draft.Deadline
	if evidence.ReferenceUnixMs != 0 || evidence.Timezone != "" || evidence.Resolution != "" || evidence.Reason != "" || evidence.ParsedUnixMs != 0 || evidence.InstructionReferenceUnixMs != 0 {
		return taskDraft{}, status.Error(codes.FailedPrecondition, "model supplied trusted deadline fields")
	}
	interpreted, err := interpretDraftDeadline(instruction, messages, draftDeadlineEvidence{Text: evidence.Text, Source: evidence.Source, SourceMessageID: evidence.SourceMessageID}, reference)
	if err != nil {
		return taskDraft{}, err
	}
	draft.Deadline = draftDeadlineMetadata{Text: interpreted.Text, Source: interpreted.Source, SourceMessageID: interpreted.SourceMessageID, ReferenceUnixMs: interpreted.ReferenceUnixMs, Timezone: interpreted.Timezone, Resolution: interpreted.Resolution, Reason: interpreted.Reason, ParsedUnixMs: interpreted.DueAtUnixMs}
	if reference != nil {
		draft.Deadline.InstructionReferenceUnixMs = *reference
	}
	draft.DueAtUnixMs = interpreted.DueAtUnixMs
	name := strings.TrimSpace(draft.AssigneeName)
	if name != "" && !assigneeMentionInAuthorizedText(instruction, messages, name) {
		return taskDraft{}, status.Error(codes.FailedPrecondition, "assignee mention could not be verified")
	}
	return draft, nil
}

func assigneeMentionInAuthorizedText(instruction string, messages []*impb.TeamGroupMessage, name string) bool {
	if strings.Contains(instruction, name) {
		return true
	}
	for _, message := range messages {
		if message != nil && message.GetContentType() == 1 && strings.Contains(message.GetContent(), name) {
			return true
		}
	}
	return false
}

func draftPreparationFingerprint(teamID, groupID int64, instruction string) string {
	return draftPreparationFingerprintWithReference(teamID, groupID, instruction, nil)
}

func draftPreparationFingerprintWithReference(teamID, groupID int64, instruction string, reference *int64) string {
	value, _ := json.Marshal(struct {
		TeamID      int64  `json:"team_id"`
		GroupID     int64  `json:"group_id"`
		Instruction string `json:"instruction"`
		Reference   *int64 `json:"instruction_reference_unix_ms,omitempty"`
	}{teamID, groupID, instruction, reference})
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func sourceInAuthorizedText(messages []*impb.TeamGroupMessage, sourceID int64) bool {
	if sourceID == 0 {
		return true
	}
	for _, message := range messages {
		if message != nil && message.GetId() == sourceID && message.GetContentType() == 1 {
			return true
		}
	}
	return false
}
