package agent

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
)

type triggerMemberRPCFunc func(context.Context, *impb.ResolveTaskTriggerMemberRequest, ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error)

func (f triggerMemberRPCFunc) ResolveTaskTriggerMember(ctx context.Context, req *impb.ResolveTaskTriggerMemberRequest, opts ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
	return f(ctx, req, opts...)
}

func (f triggerMemberRPCFunc) ReadTaskTriggerContext(context.Context, *impb.ReadTaskTriggerContextRequest, ...grpc.CallOption) (*impb.ReadTaskTriggerContextResponse, error) {
	return nil, status.Error(codes.Unimplemented, "no source read fallback")
}

func triggerMemberResponse(id int64, name string, count int, truncated bool) *impb.ResolveTaskTriggerMemberResponse {
	source := validTriggerClientResponse(id)
	response := &impb.ResolveTaskTriggerMemberResponse{MessageId: id, ActorId: source.ActorId, TeamId: source.TeamId,
		GroupId: source.GroupId, RequestKey: source.RequestKey, Name: name, Truncated: truncated}
	for n := 0; n < count; n++ {
		response.Candidates = append(response.Candidates, &impb.TaskTriggerMemberCandidate{UserId: 9007199254741013 + int64(n), Username: fmt.Sprintf("member-%d", n), Nickname: name})
	}
	return response
}

func triggerAssigneeSource(name string) *impb.ReadTaskTriggerContextResponse {
	source := validTriggerClientResponse(triggerClientSourceID)
	source.Instruction = "请" + name + "整理两项任务"
	source.Messages[0].Content = "@AI 整理任务 " + source.Instruction
	return source
}

func triggerAssigneeGeneratedDraft(name string) taskDraft {
	return taskDraft{Title: "修复缓存", Description: "先核对旧值，再回归", AssigneeName: name, SourceMessageID: triggerClientSourceID - 1,
		DueAtUnixMs: 1791003600123, Deadline: draftDeadlineMetadata{Text: "明天15:30", Source: "instruction", ReferenceUnixMs: 1791000000000,
			Timezone: "Asia/Shanghai", Resolution: "parsed", ParsedUnixMs: 1791003600123, InstructionReferenceUnixMs: 1791000000000}}
}

func TestTriggerMemberReadOnlySendsSourceAndLiteralWithEmptyMetadataAndSmallCallBudget(t *testing.T) {
	for _, id := range []int64{triggerClientSourceID, math.MaxInt64} {
		client := &TriggerContextClient{rpc: triggerMemberRPCFunc(func(ctx context.Context, req *impb.ResolveTaskTriggerMemberRequest, opts ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
			md, ok := metadata.FromOutgoingContext(ctx)
			deadline, bounded := ctx.Deadline()
			if !ok || len(md) != 0 || !bounded || time.Until(deadline) > 5*time.Second || req.MessageId != id || req.Name != "张三" {
				t.Fatalf("identity escaped: %v %v", req, md)
			}
			if len(opts) != 1 {
				t.Fatalf("expected a per-call receive bound: %v", opts)
			}
			limit, ok := opts[0].(grpc.MaxRecvMsgSizeCallOption)
			if !ok || limit.MaxRecvMsgSize != model.AgentTriggerMemberResponseLimit {
				t.Fatalf("wrong receive bound: %v", opts)
			}
			response := triggerMemberResponse(id, req.Name, 1, false)
			response.Candidates[0].UserId = math.MaxInt64
			return response, nil
		})}
		ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer private", "actor-id", "1", "team-id", "2", "group-id", "3", "service", "user", "x-extra", "private"))
		response, err := client.ResolveMember(ctx, id, "张三")
		if err != nil || response.GetMessageId() != id || response.GetCandidates()[0].GetUserId() != math.MaxInt64 {
			t.Fatalf("large ID changed: %v %v", response, err)
		}
	}
}

func TestTriggerMemberReadRejectsMalformedSuccessWithoutLeakingNameOrScope(t *testing.T) {
	for name, change := range map[string]func(*impb.ResolveTaskTriggerMemberResponse){
		"wrong message": func(r *impb.ResolveTaskTriggerMemberResponse) { r.MessageId++ },
		"wrong name":    func(r *impb.ResolveTaskTriggerMemberResponse) { r.Name = "李四" },
		"zero actor":    func(r *impb.ResolveTaskTriggerMemberResponse) { r.ActorId = 0 },
		"negative team": func(r *impb.ResolveTaskTriggerMemberResponse) { r.TeamId = -1 },
		"zero group":    func(r *impb.ResolveTaskTriggerMemberResponse) { r.GroupId = 0 },
		"missing key":   func(r *impb.ResolveTaskTriggerMemberResponse) { r.RequestKey = "" },
		"wrong key":     func(r *impb.ResolveTaskTriggerMemberResponse) { r.RequestKey += ":0" },
		"too many candidates": func(r *impb.ResolveTaskTriggerMemberResponse) {
			r.Candidates = triggerMemberResponse(r.MessageId, r.Name, 21, false).Candidates
		},
		"truncated none": func(r *impb.ResolveTaskTriggerMemberResponse) { r.Truncated = true; r.Candidates = nil },
		"truncated one":  func(r *impb.ResolveTaskTriggerMemberResponse) { r.Truncated = true },
		"truncated nineteen": func(r *impb.ResolveTaskTriggerMemberResponse) {
			r.Truncated = true
			r.Candidates = triggerMemberResponse(r.MessageId, r.Name, 19, false).Candidates
		},
		"nil candidate":     func(r *impb.ResolveTaskTriggerMemberResponse) { r.Candidates[0] = nil },
		"zero candidate ID": func(r *impb.ResolveTaskTriggerMemberResponse) { r.Candidates[0].UserId = 0 },
		"duplicate ID":      func(r *impb.ResolveTaskTriggerMemberResponse) { r.Candidates = append(r.Candidates, r.Candidates[0]) },
		"descending IDs": func(r *impb.ResolveTaskTriggerMemberResponse) {
			r.Candidates = append(r.Candidates, &impb.TaskTriggerMemberCandidate{UserId: 1, Username: "张三"})
		},
		"no literal match":  func(r *impb.ResolveTaskTriggerMemberResponse) { r.Candidates[0].Nickname = "李四" },
		"empty username":    func(r *impb.ResolveTaskTriggerMemberResponse) { r.Candidates[0].Username = "" },
		"bad username UTF8": func(r *impb.ResolveTaskTriggerMemberResponse) { r.Candidates[0].Username = "\xff" },
		"long username":     func(r *impb.ResolveTaskTriggerMemberResponse) { r.Candidates[0].Username = strings.Repeat("中", 65) },
		"bad nickname UTF8": func(r *impb.ResolveTaskTriggerMemberResponse) { r.Candidates[0].Nickname = "\xff" },
		"long nickname": func(r *impb.ResolveTaskTriggerMemberResponse) {
			r.Candidates[0].Username = r.Name
			r.Candidates[0].Nickname = strings.Repeat("中", 65)
		},
		"oversize proto": func(r *impb.ResolveTaskTriggerMemberResponse) {
			r.ProtoReflect().SetUnknown(protowire.AppendBytes(protowire.AppendTag(nil, 9, protowire.BytesType), bytes.Repeat([]byte("x"), model.AgentTriggerMemberResponseLimit)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := triggerMemberResponse(triggerClientSourceID, "张三", 1, false)
			change(response)
			client := &TriggerContextClient{rpc: triggerMemberRPCFunc(func(context.Context, *impb.ResolveTaskTriggerMemberRequest, ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
				return response, nil
			})}
			data, err := client.ResolveMember(context.Background(), triggerClientSourceID, "张三")
			if data != nil || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "张三") || strings.Contains(err.Error(), "900719") {
				t.Fatalf("malformed success trusted: %v %v", data, err)
			}
		})
	}
	client := &TriggerContextClient{rpc: triggerMemberRPCFunc(func(context.Context, *impb.ResolveTaskTriggerMemberRequest, ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
		return nil, nil
	})}
	if data, err := client.ResolveMember(context.Background(), triggerClientSourceID, "张三"); data != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("nil success trusted: %v %v", data, err)
	}
}

func TestTriggerMemberReadAcceptsTwentyExactMatchesAndSixtyFourRuneLiteral(t *testing.T) {
	name := strings.Repeat("中", 64)
	for _, truncated := range []bool{false, true} {
		client := &TriggerContextClient{rpc: triggerMemberRPCFunc(func(context.Context, *impb.ResolveTaskTriggerMemberRequest, ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
			return triggerMemberResponse(triggerClientSourceID, name, 20, truncated), nil
		})}
		if result, err := client.ResolveMember(context.Background(), triggerClientSourceID, name); err != nil || len(result.GetCandidates()) != 20 || result.GetTruncated() != truncated {
			t.Fatalf("valid boundary rejected: %v %v", result, err)
		}
	}
}

func TestTriggerMemberResponseValidatorRequiresCanonicalExpectedLiteralAndPositiveSource(t *testing.T) {
	for _, tc := range []struct {
		id   int64
		name string
	}{
		{0, "张三"}, {-1, "张三"}, {triggerClientSourceID, ""}, {triggerClientSourceID, " 张三"}, {triggerClientSourceID, "张三 "},
		{triggerClientSourceID, "\xff"}, {triggerClientSourceID, strings.Repeat("中", 65)},
	} {
		response := triggerMemberResponse(tc.id, tc.name, 0, false)
		if validTriggerMemberResponse(response, tc.id, tc.name) {
			t.Fatalf("empty list bypassed expected request shape: id=%d", tc.id)
		}
	}
	response := triggerMemberResponse(triggerClientSourceID, "张三", 0, false)
	if !validTriggerMemberResponse(response, triggerClientSourceID, "张三") {
		t.Fatal("legitimate not-found response rejected")
	}
}

func TestTriggerMemberReadRejectsLocalInputAndPreservesCallerCancellation(t *testing.T) {
	client := &TriggerContextClient{rpc: triggerMemberRPCFunc(func(context.Context, *impb.ResolveTaskTriggerMemberRequest, ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
		t.Fatal("bad local input reached RPC")
		return nil, nil
	})}
	for _, name := range []string{"", " 张三", "张三 ", "\xff", strings.Repeat("中", 65)} {
		if _, err := client.ResolveMember(context.Background(), triggerClientSourceID, name); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	if _, err := client.ResolveMember(context.Background(), 0, "张三"); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	if _, err := client.ResolveMember(nil, triggerClientSourceID, "张三"); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	for _, absent := range []*TriggerContextClient{nil, {}} {
		if _, err := absent.ResolveMember(context.Background(), triggerClientSourceID, "张三"); status.Code(err) != codes.Unavailable {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := absent.ResolveMember(ctx, 0, ""); status.Code(err) != codes.Canceled {
			t.Fatalf("caller cancel lost priority: %v", err)
		}
	}
}

func TestTriggerMemberReadPreservesExpectedStatusWithoutRetriesOrPrivateDetails(t *testing.T) {
	for _, code := range []codes.Code{codes.InvalidArgument, codes.NotFound, codes.PermissionDenied, codes.Unauthenticated, codes.Canceled, codes.DeadlineExceeded, codes.Unavailable, codes.Internal, codes.Unimplemented, codes.ResourceExhausted} {
		calls := 0
		client := &TriggerContextClient{rpc: triggerMemberRPCFunc(func(context.Context, *impb.ResolveTaskTriggerMemberRequest, ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
			calls++
			return nil, status.Error(code, "private SQL/TLS/token/name")
		})}
		data, err := client.ResolveMember(context.Background(), triggerClientSourceID, "张三")
		want := codes.Unavailable
		if code == codes.InvalidArgument || code == codes.NotFound || code == codes.PermissionDenied || code == codes.Unauthenticated || code == codes.Canceled || code == codes.DeadlineExceeded {
			want = code
		}
		if data != nil || status.Code(err) != want || strings.Contains(err.Error(), "private") || calls != 1 {
			t.Fatalf("unsafe result for %s: %v", code, err)
		}
	}
}

func TestTriggerMemberReadUsesFiveSecondsAndHonorsShorterCallerDeadline(t *testing.T) {
	client := &TriggerContextClient{rpc: triggerMemberRPCFunc(func(ctx context.Context, req *impb.ResolveTaskTriggerMemberRequest, _ ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
		<-ctx.Done()
		return triggerMemberResponse(req.MessageId, req.Name, 1, false), nil
	})}
	started := time.Now()
	if result, err := client.ResolveMember(context.Background(), triggerClientSourceID, "张三"); result != nil || status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("late success: %v %v", result, err)
	}
	if elapsed := time.Since(started); elapsed < 4900*time.Millisecond || elapsed > 6*time.Second {
		t.Fatalf("five-second bound: %v", elapsed)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started = time.Now()
	if _, err := client.ResolveMember(ctx, triggerClientSourceID, "张三"); status.Code(err) != codes.DeadlineExceeded {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("caller deadline extended")
	}
}

func TestTriggerAssigneeResolutionKeepsAllOtherFieldsForFiveResolutionStates(t *testing.T) {
	for _, tc := range []struct {
		name      string
		count     int
		truncated bool
		want      draftAssigneeResolution
	}{
		{"", 0, false, assigneeNone}, {"张三", 0, false, assigneeNotFound}, {"张三", 1, false, assigneeMatched},
		{"张三", 2, false, assigneeAmbiguous}, {"张三", 20, false, assigneeAmbiguous}, {"张三", 20, true, assigneeTruncated},
	} {
		t.Run(string(tc.want)+fmt.Sprint(tc.count), func(t *testing.T) {
			calls := 0
			client := &TriggerContextClient{rpc: triggerMemberRPCFunc(func(ctx context.Context, req *impb.ResolveTaskTriggerMemberRequest, _ ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
				calls++
				md, _ := metadata.FromOutgoingContext(ctx)
				if len(md) != 0 || req.MessageId != triggerClientSourceID || req.Name != tc.name {
					t.Fatal("scope or caller metadata changed")
				}
				return triggerMemberResponse(req.MessageId, req.Name, tc.count, tc.truncated), nil
			})}
			draft := triggerAssigneeGeneratedDraft(" \t" + tc.name + " \n")
			expected := draft
			expected.AssigneeName, expected.AssigneeResolution = tc.name, tc.want
			if tc.want == assigneeMatched {
				expected.AssigneeID = 9007199254741013
			}
			result, err := client.resolveAssignee(context.Background(), triggerAssigneeSource(tc.name), draft)
			if err != nil || result != expected || tc.name == "" && calls != 0 || tc.name != "" && calls != 1 {
				t.Fatalf("result=%+v want=%+v err=%v calls=%d", result, expected, err, calls)
			}
		})
	}
	var absent *TriggerContextClient
	if result, err := absent.resolveAssignee(context.Background(), triggerAssigneeSource(""), triggerAssigneeGeneratedDraft("")); err != nil || result.AssigneeResolution != assigneeNone {
		t.Fatalf("empty literal required RPC: %v", err)
	}
}

func TestTriggerAssigneeRejectsModelTrustedFieldsAndHallucinationBeforeRPC(t *testing.T) {
	client := &TriggerContextClient{rpc: triggerMemberRPCFunc(func(context.Context, *impb.ResolveTaskTriggerMemberRequest, ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
		t.Fatal("untrusted output reached RPC")
		return nil, nil
	})}
	for _, draft := range []taskDraft{triggerAssigneeGeneratedDraft("不存在的人"), {AssigneeID: 1}, {AssigneeID: -1}, {AssigneeName: "张三", AssigneeResolution: assigneeMatched},
		{AssigneeName: "张三", AssigneeResolution: assigneeNone}, {AssigneeName: "\xff"}, {AssigneeName: strings.Repeat("张", 65)}} {
		result, err := client.resolveAssignee(context.Background(), triggerAssigneeSource("张三"), draft)
		if result != (taskDraft{}) || status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("untrusted generated fields accepted: %+v %v", result, err)
		}
	}
	for _, contentType := range []int32{2, 3, 4} {
		source := triggerAssigneeSource("")
		source.Messages[1].Content, source.Messages[1].ContentType = "张三做任务", contentType
		if result, err := client.resolveAssignee(context.Background(), source, triggerAssigneeGeneratedDraft("张三")); result != (taskDraft{}) || status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("nontext evidence accepted: %+v %v", result, err)
		}
	}
}

func TestTriggerAssigneeAcceptsNameFromAuthorizedTextHistory(t *testing.T) {
	source := triggerAssigneeSource("")
	source.Messages[1].Content, source.Messages[1].ContentType = "张三先核对缓存", 1
	client := &TriggerContextClient{rpc: triggerMemberRPCFunc(func(context.Context, *impb.ResolveTaskTriggerMemberRequest, ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
		return triggerMemberResponse(triggerClientSourceID, "张三", 1, false), nil
	})}
	if result, err := client.resolveAssignee(context.Background(), source, triggerAssigneeGeneratedDraft("张三")); err != nil || result.AssigneeID != 9007199254741013 || result.AssigneeResolution != assigneeMatched {
		t.Fatalf("authorized history literal rejected: %+v %v", result, err)
	}
}

func TestTriggerAssigneeRejectsInvalidSourceAndChangedReplyScopeWithZeroDraft(t *testing.T) {
	if result, err := (*TriggerContextClient)(nil).resolveAssignee(context.Background(), nil, triggerAssigneeGeneratedDraft("")); result != (taskDraft{}) || status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("empty assignee bypassed source verification: %+v %v", result, err)
	}
	for _, invalid := range []string{"nil", "changed source time", "bad source instruction", "future history", "bad key"} {
		source := triggerAssigneeSource("张三")
		switch invalid {
		case "nil":
			source = nil
		case "changed source time":
			source.Messages[0].CreatedAtUnixMs++
		case "bad source instruction":
			source.Instruction += " changed"
		case "future history":
			source.Messages[1].Id = source.MessageId + 1
		case "bad key":
			source.RequestKey += ":0"
		}
		client := &TriggerContextClient{rpc: triggerMemberRPCFunc(func(context.Context, *impb.ResolveTaskTriggerMemberRequest, ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
			t.Fatal("bad source reached resolver")
			return nil, nil
		})}
		if result, err := client.resolveAssignee(context.Background(), source, triggerAssigneeGeneratedDraft("张三")); result != (taskDraft{}) || status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("bad source accepted %s: %+v %v", invalid, result, err)
		}
	}
	for _, field := range []string{"message", "actor", "team", "group", "key"} {
		response := triggerMemberResponse(triggerClientSourceID, "张三", 1, false)
		switch field {
		case "message":
			response.MessageId++
		case "actor":
			response.ActorId++
		case "team":
			response.TeamId++
		case "group":
			response.GroupId++
		case "key":
			response.RequestKey += "changed"
		}
		client := &TriggerContextClient{rpc: triggerMemberRPCFunc(func(context.Context, *impb.ResolveTaskTriggerMemberRequest, ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
			return response, nil
		})}
		if result, err := client.resolveAssignee(context.Background(), triggerAssigneeSource("张三"), triggerAssigneeGeneratedDraft("张三")); result != (taskDraft{}) || status.Code(err) != codes.Unavailable {
			t.Fatalf("changed %s scope accepted: %+v %v", field, result, err)
		}
	}
}

func TestTriggerAssigneePropagatesCurrentSourceAndPermissionFailureWithoutSuccess(t *testing.T) {
	for _, code := range []codes.Code{codes.NotFound, codes.PermissionDenied, codes.Unauthenticated, codes.Unavailable} {
		client := &TriggerContextClient{rpc: triggerMemberRPCFunc(func(context.Context, *impb.ResolveTaskTriggerMemberRequest, ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
			return nil, status.Error(code, "private revoked scope")
		})}
		if result, err := client.resolveAssignee(context.Background(), triggerAssigneeSource("张三"), triggerAssigneeGeneratedDraft("张三")); result != (taskDraft{}) || status.Code(err) != code || strings.Contains(err.Error(), "private") {
			t.Fatalf("failed lookup became draft: %+v %v", result, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := (*TriggerContextClient)(nil).resolveAssignee(ctx, nil, taskDraft{}); result != (taskDraft{}) || status.Code(err) != codes.Canceled {
		t.Fatalf("cancellation priority: %+v %v", result, err)
	}
	if _, err := (*TriggerContextClient)(nil).resolveAssignee(nil, nil, taskDraft{}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
}

type triggerMemberTLSStub struct {
	impb.UnimplementedIMTriggerServer
	resolve triggerMemberRPCFunc
}

func (s *triggerMemberTLSStub) ResolveTaskTriggerMember(ctx context.Context, req *impb.ResolveTaskTriggerMemberRequest) (*impb.ResolveTaskTriggerMemberResponse, error) {
	return s.resolve(ctx, req)
}
func (s *triggerMemberTLSStub) ReadTaskTriggerContext(_ context.Context, req *impb.ReadTaskTriggerContextRequest) (*impb.ReadTaskTriggerContextResponse, error) {
	source := validTriggerClientResponse(req.MessageId)
	source.Messages[1].Content = strings.Repeat("x", 65535)
	return source, nil
}

func TestTriggerMemberActualMTLSUsesDedicatedRoleAndPerCallSizeWithoutChangingContextRead(t *testing.T) {
	files := botClientCertificates(t)
	creds, err := rpcauth.NewServiceServerCredentials(files["im.go-im.internal"], "agent.go-im.internal")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(creds))
	var calls atomic.Int32
	impb.RegisterIMTriggerServer(server, &triggerMemberTLSStub{resolve: func(ctx context.Context, req *impb.ResolveTaskTriggerMemberRequest, _ ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
		if err := rpcauth.RequireServiceIdentity(ctx, "agent.go-im.internal"); err != nil {
			return nil, err
		}
		md, _ := metadata.FromIncomingContext(ctx)
		for _, key := range []string{"authorization", "actor-id", "team-id", "group-id", "service", "x-extra"} {
			if len(md.Get(key)) != 0 {
				t.Errorf("forwarded caller field %s", key)
			}
		}
		response := triggerMemberResponse(req.MessageId, req.Name, 1, false)
		switch calls.Add(1) {
		case 1:
			return response, nil
		case 2:
			response.ProtoReflect().SetUnknown(protowire.AppendBytes(protowire.AppendTag(nil, 9, protowire.BytesType), bytes.Repeat([]byte("x"), model.AgentTriggerMemberResponseLimit+1)))
			return response, nil
		default:
			return nil, status.Error(codes.Unavailable, "private unavailable SQL")
		}
	}})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	client, err := NewTriggerContextClient(triggerContextEnvironment(listener.Addr().String(), "im.go-im.internal", files["agent.go-im.internal"]))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer private", "actor-id", "1", "team-id", "2", "group-id", "3", "service", "user", "x-extra", "private"))
	if result, err := client.ResolveMember(ctx, triggerClientSourceID, "张三"); err != nil || result.GetCandidates()[0].GetUserId() != 9007199254741013 {
		t.Fatalf("new method failed: %v %v", result, err)
	}
	if result, err := client.ResolveMember(ctx, triggerClientSourceID, "张三"); result != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("oversize reply trusted: %v %v", result, err)
	}
	if result, err := client.ResolveMember(ctx, triggerClientSourceID, "张三"); result != nil || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe availability error: %v %v", result, err)
	}
	if calls.Load() != 3 {
		t.Fatalf("automatic RPC retry: %d", calls.Load())
	}
	if result, err := client.Read(ctx, triggerClientSourceID); err != nil || len(result.GetMessages()[1].GetContent()) != 65535 {
		t.Fatalf("new call changed old 2MiB read: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ResolveMember(context.Background(), triggerClientSourceID, "张三"); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.ResolveMember(cancelled, triggerClientSourceID, "张三"); status.Code(err) != codes.Canceled {
		t.Fatal(err)
	}
}

func TestTriggerMemberConnectionCloseDuringCallRemainsUnavailableUnlessCallerCancelled(t *testing.T) {
	files := botClientCertificates(t)
	for _, cancelCaller := range []bool{false, true} {
		client, err := NewTriggerContextClient(triggerContextEnvironment("127.0.0.1:9006", "im.go-im.internal", files["agent.go-im.internal"]))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		client.rpc = triggerMemberRPCFunc(func(context.Context, *impb.ResolveTaskTriggerMemberRequest, ...grpc.CallOption) (*impb.ResolveTaskTriggerMemberResponse, error) {
			if cancelCaller {
				cancel()
			}
			_ = client.Close()
			return nil, status.Error(codes.Canceled, "private connection closing")
		})
		result, err := client.ResolveMember(ctx, triggerClientSourceID, "张三")
		cancel()
		want := codes.Unavailable
		if cancelCaller {
			want = codes.Canceled
		}
		if result != nil || status.Code(err) != want || strings.Contains(err.Error(), "private") {
			t.Fatalf("connection vs caller cancellation: %v %v", result, err)
		}
	}
}
