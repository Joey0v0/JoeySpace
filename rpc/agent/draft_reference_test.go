package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDraftReferenceKeepsLegacyFingerprint(t *testing.T) {
	// Exact pre-reference JSON bytes: older persisted request keys must replay.
	sum := sha256.Sum256([]byte(`{"team_id":200,"group_id":300,"instruction":"提取待办"}`))
	want := hex.EncodeToString(sum[:])
	if got := draftPreparationFingerprintWithReference(200, 300, "提取待办", nil); got != want {
		t.Fatalf("changed legacy request identity: %s != %s", got, want)
	}
	reference := int64(1791043199123)
	first := draftPreparationFingerprintWithReference(200, 300, "提取待办", &reference)
	if first == want {
		t.Fatal("reference was omitted from new identity")
	}
	reference++
	if draftPreparationFingerprintWithReference(200, 300, "提取待办", &reference) == first {
		t.Fatal("changed reference did not change request identity")
	}
}

func TestDraftReferenceRetryAndConflictOverRPC(t *testing.T) {
	p := testDraftPreparer(t)
	var savedFingerprint string
	var generated int
	p.generator = taskDraftGeneratorFunc(func(context.Context, string, []*impb.TeamGroupMessage) (taskDraft, error) {
		generated++
		if generated == 1 {
			return taskDraft{Deadline: draftDeadlineMetadata{Source: "none"}}, status.Error(codes.Unavailable, "first generation failed")
		}
		return taskDraft{Deadline: draftDeadlineMetadata{Source: "none"}, Title: "修复缓存", SourceMessageID: 600}, nil
	})
	p.store = draftPreparationStoreFuncs{
		find: func(_ context.Context, _ draftRunScope, key, fingerprint string) (int64, error) {
			if key != "request-1" {
				t.Fatalf("request key changed: %s", key)
			}
			if savedFingerprint == "" {
				return 0, nil
			}
			if fingerprint != savedFingerprint {
				return 0, status.Error(codes.AlreadyExists, "changed reference")
			}
			return 8123, nil
		},
		save: func(_ context.Context, _ int64, _ draftRunScope, _ taskDraft, _, fingerprint string) (int64, error) {
			savedFingerprint = fingerprint
			return 8123, nil
		},
	}
	client := testPrepareDraftClient(t, p)
	reference := int64(1791043199123)
	req := &pb.PrepareTaskDraftRequest{TeamId: 200, GroupId: 300, Instruction: "提取待办", InstructionReferenceUnixMs: &reference}
	call := func(want codes.Code) {
		t.Helper()
		ctx, cancel := preparedRPCContext("authorization", "Bearer user-token", "idempotency-key", "request-1")
		defer cancel()
		resp, err := client.PrepareTaskDraft(ctx, req)
		if status.Code(err) != want {
			t.Fatalf("prepare = %v, %v; want %v", resp, err, want)
		}
		if want == codes.OK && resp.GetRunId() != 8123 {
			t.Fatalf("changed run ID: %v", resp)
		}
	}
	call(codes.Unavailable)
	call(codes.OK)
	if savedFingerprint != draftPreparationFingerprintWithReference(200, 300, "提取待办", &reference) {
		t.Fatal("lost reference before persistence")
	}
	call(codes.OK)
	reference++
	call(codes.AlreadyExists)
	req.InstructionReferenceUnixMs = nil
	call(codes.AlreadyExists)
	if generated != 2 {
		t.Fatalf("replay or conflict called model: %d", generated)
	}
}

func TestDraftReferenceRejectsInvalidBeforeBusinessCalls(t *testing.T) {
	for _, value := range []int64{0, -1, maxDraftDueAtUnixMs + 1} {
		p := testDraftPreparer(t)
		p.identity = &draftIdentityResolver{} // Any identity call would fail differently.
		id, err := p.prepareWithReference(context.Background(), "user-token", 200, 300, "提取待办", "request-1", &value)
		if id != 0 || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid reference %d: %d, %v", value, id, err)
		}
	}
}
