package agent

import (
	"context"
	"errors"

	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type triggerTaskSource interface {
	Read(context.Context, int64) (*impb.ReadTaskTriggerContextResponse, error)
	resolveAssignee(context.Context, *impb.ReadTaskTriggerContextResponse, taskDraft) (taskDraft, error)
}

type triggerTaskResultStore interface {
	BeginModel(context.Context, TriggerLease) (bool, error)
	CompleteDraftCollection(context.Context, TriggerLease, int64, draftRunScope, []taskDraft, string, string) (int64, error)
}

type triggerTaskProcessor struct {
	source    triggerTaskSource
	generator taskDraftCollectionGenerator
	store     triggerTaskResultStore
	nextRunID func() int64
}

// Reuse the configured Eino generator and ID node, never a second model client.
func NewTriggerTaskProcessor(server *Server, source *TriggerContextClient, store *TriggerInboxStore) (TriggerProcessor, error) {
	if server == nil || server.preparer == nil || server.preparer.idNode == nil || source == nil || source.rpc == nil || store == nil || store.db == nil {
		return nil, status.Error(codes.Unavailable, "trigger task processor is not configured")
	}
	generator, ok := server.preparer.generator.(taskDraftCollectionGenerator)
	if !ok || generator == nil {
		return nil, status.Error(codes.Unavailable, "trigger collection generator is not configured")
	}
	if eino, ok := generator.(*EinoTaskDraftGenerator); ok && (eino == nil || eino.chain == nil) {
		return nil, status.Error(codes.Unavailable, "trigger collection generator is not configured")
	}
	node := server.preparer.idNode
	return &triggerTaskProcessor{source: source, generator: generator, store: store, nextRunID: func() int64 { return node.Generate().Int64() }}, nil
}

// Process never claims, renews, releases, retries a model, creates a Task or
// posts a reply. Its only write after accounting is one atomic draft result.
func (p *triggerTaskProcessor) Process(ctx context.Context, lease TriggerLease) error {
	if err := validateTriggerLeaseInput(ctx, lease); err != nil {
		return err
	}
	if p == nil || p.source == nil || p.generator == nil || p.store == nil || p.nextRunID == nil {
		return status.Error(codes.Unavailable, "trigger task processor is not configured")
	}
	source, err := p.readSource(ctx, lease.MessageID, nil)
	if err != nil {
		return err
	}
	granted, err := p.store.BeginModel(ctx, lease)
	if ctx.Err() != nil || err != nil {
		return triggerProcessorError(ctx, err)
	}
	if !granted {
		return status.Error(codes.FailedPrecondition, "trigger model permission was not granted")
	}
	// Neither a generator nor a reused response object may mutate saved facts.
	modelContext := proto.Clone(source).(*impb.ReadTaskTriggerContextResponse)
	candidates, err := p.generator.GenerateDrafts(ctx, source.Instruction, modelContext.Messages)
	if ctx.Err() != nil || err != nil {
		return triggerProcessorError(ctx, err)
	}
	authorized, err := p.readSource(ctx, lease.MessageID, source)
	if err != nil {
		return err
	}
	if len(candidates) < 1 || len(candidates) > maxGeneratedTaskDrafts {
		return status.Error(codes.FailedPrecondition, "invalid generated draft collection")
	}
	scope := draftRunScope{TeamID: source.TeamId, GroupID: source.GroupId, InitiatorID: source.ActorId}
	reference := source.ReferenceTimeUnixMs
	proofs := make([]taskDraft, len(candidates))
	items := make([]taskDraft, len(candidates))
	for i, candidate := range candidates {
		originalProof, err := verifyGeneratedDraftEvidence(ctx, scope, source.Instruction, source.Messages, candidate, &reference)
		if err != nil {
			return triggerProcessorError(ctx, err)
		}
		proofs[i], err = verifyGeneratedDraftEvidence(ctx, scope, source.Instruction, authorized.Messages, candidate, &reference)
		if err != nil {
			return triggerProcessorError(ctx, err)
		}
		if proofs[i] != originalProof {
			return status.Error(codes.FailedPrecondition, "trigger draft evidence changed")
		}
		resolved, err := p.source.resolveAssignee(ctx, authorized, proofs[i])
		if ctx.Err() != nil || err != nil {
			return triggerProcessorError(ctx, err)
		}
		run, err := newWaitingTaskDraftRun(scope, resolved)
		if err != nil {
			return status.Error(codes.FailedPrecondition, "invalid generated task draft")
		}
		items[i] = run.Draft
	}
	final, err := p.readSource(ctx, lease.MessageID, source)
	if err != nil {
		return err
	}
	// Recheck literal evidence without further member calls: a removed or
	// changed referenced message cannot silently replace the verified deadline.
	for i, candidate := range candidates {
		proof, err := verifyGeneratedDraftEvidence(ctx, scope, source.Instruction, final.Messages, candidate, &reference)
		if err != nil {
			return triggerProcessorError(ctx, err)
		}
		if proof != proofs[i] {
			return status.Error(codes.FailedPrecondition, "trigger draft evidence changed")
		}
	}
	if ctx.Err() != nil {
		return triggerProcessorError(ctx, ctx.Err())
	}
	runID := p.nextRunID()
	if runID <= 0 {
		return status.Error(codes.Unavailable, "trigger run identity unavailable")
	}
	fingerprint := draftCollectionFingerprint(scope.TeamID, scope.GroupID, source.Instruction, &reference)
	if ctx.Err() != nil {
		return triggerProcessorError(ctx, ctx.Err())
	}
	savedID, err := p.store.CompleteDraftCollection(ctx, lease, runID, scope, items, source.RequestKey, fingerprint)
	if err != nil {
		return triggerProcessorError(ctx, err)
	}
	if savedID != runID {
		return status.Error(codes.Unavailable, "trigger draft result unavailable")
	}
	return nil
}

func (p *triggerTaskProcessor) readSource(ctx context.Context, messageID int64, fixed *impb.ReadTaskTriggerContextResponse) (*impb.ReadTaskTriggerContextResponse, error) {
	response, err := p.source.Read(ctx, messageID)
	if ctx.Err() != nil || err != nil {
		return nil, triggerProcessorError(ctx, err)
	}
	if !validTriggerContext(response, messageID) {
		return nil, status.Error(codes.Unavailable, "invalid persisted trigger context")
	}
	if fixed != nil && (response.MessageId != fixed.MessageId || response.MsgId != fixed.MsgId || response.ActorId != fixed.ActorId ||
		response.TeamId != fixed.TeamId || response.GroupId != fixed.GroupId || response.Instruction != fixed.Instruction ||
		response.ReferenceTimeUnixMs != fixed.ReferenceTimeUnixMs || response.RequestKey != fixed.RequestKey) {
		return nil, status.Error(codes.FailedPrecondition, "persisted trigger source changed")
	}
	return proto.Clone(response).(*impb.ReadTaskTriggerContextResponse), nil
}

func triggerProcessorError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if errors.Is(err, ErrTriggerLeaseLost) {
		return ErrTriggerLeaseLost
	}
	if errors.Is(err, ErrInvalidTriggerState) {
		return ErrInvalidTriggerState
	}
	code := status.Code(err)
	if errors.Is(err, context.Canceled) {
		code = codes.Canceled
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = codes.DeadlineExceeded
	}
	switch code {
	case codes.InvalidArgument, codes.Unauthenticated, codes.PermissionDenied, codes.NotFound, codes.FailedPrecondition, codes.Canceled, codes.DeadlineExceeded:
	default:
		code = codes.Unavailable
	}
	return status.Error(code, "trigger task processing failed")
}
