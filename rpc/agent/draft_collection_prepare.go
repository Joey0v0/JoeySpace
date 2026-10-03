package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"

	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type taskDraftCollectionGenerator interface {
	GenerateDrafts(context.Context, string, []*impb.TeamGroupMessage) ([]taskDraft, error)
}

type draftCollectionSaver interface {
	saveWaitingDraftCollection(context.Context, int64, draftRunScope, []taskDraft, string, string) (int64, error)
}

func (p *draftPreparer) prepareCollectionWithReference(ctx context.Context, token string, teamID, groupID int64, instruction, requestKey string, reference *int64) (int64, error) {
	if p == nil || p.identity == nil || p.messages == nil || p.store == nil || p.idNode == nil {
		return 0, status.Error(codes.Unavailable, "draft preparation is not configured")
	}
	generator, canGenerate := p.generator.(taskDraftCollectionGenerator)
	store, canSave := p.store.(draftCollectionSaver)
	if !canGenerate || !canSave {
		return 0, status.Error(codes.Unavailable, "draft collection preparation is not configured")
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
	fingerprint := draftCollectionFingerprint(teamID, groupID, instruction, reference)
	if id, err := p.store.findExistingDraft(ctx, scope, requestKey, fingerprint); err != nil || id > 0 {
		return id, err
	}
	drafts, err := generator.GenerateDrafts(ctx, instruction, messages)
	if ctx.Err() != nil {
		return 0, status.FromContextError(ctx.Err()).Err()
	}
	if err != nil {
		if status.Code(err) == codes.FailedPrecondition {
			return 0, status.Error(codes.FailedPrecondition, "model returned invalid draft collection")
		}
		return 0, status.Error(codes.Unavailable, "draft generator unavailable")
	}
	drafts, err = p.verifyGeneratedDrafts(ctx, token, scope, instruction, messages, drafts, reference)
	if err != nil {
		return 0, err
	}
	// Normalize editable text after all model evidence has been verified.
	for i := range drafts {
		run, err := newWaitingTaskDraftRun(scope, drafts[i])
		if err != nil {
			return 0, status.Error(codes.FailedPrecondition, "generated draft is invalid")
		}
		drafts[i] = run.Draft
	}
	return store.saveWaitingDraftCollection(ctx, p.idNode.Generate().Int64(), scope, drafts, requestKey, fingerprint)
}

func draftCollectionFingerprint(teamID, groupID int64, instruction string, reference *int64) string {
	value, _ := json.Marshal(struct {
		TeamID      int64  `json:"team_id"`
		GroupID     int64  `json:"group_id"`
		Instruction string `json:"instruction"`
		Mode        string `json:"mode"`
		Reference   *int64 `json:"instruction_reference_unix_ms,omitempty"`
	}{teamID, groupID, strings.TrimSpace(instruction), "collection", reference})
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
