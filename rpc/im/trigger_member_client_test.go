package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
)

type triggerMemberTestRPC func(context.Context, *userpb.ResolveTriggerTeamMemberRequest, ...grpc.CallOption) (*userpb.ResolveTriggerTeamMemberResponse, error)

func (f triggerMemberTestRPC) ResolveTriggerTeamMember(ctx context.Context, req *userpb.ResolveTriggerTeamMemberRequest, options ...grpc.CallOption) (*userpb.ResolveTriggerTeamMemberResponse, error) {
	return f(ctx, req, options...)
}

func (triggerMemberTestRPC) CheckTriggerTeamMember(context.Context, *userpb.CheckTriggerTeamMemberRequest, ...grpc.CallOption) (*userpb.CheckTriggerTeamMemberResponse, error) {
	return nil, errors.New("member resolver must never fall back to Check")
}

func triggerMemberTestPadding(size int) []byte {
	data := protowire.AppendTag(nil, 100, protowire.BytesType)
	return protowire.AppendBytes(data, []byte(strings.Repeat("x", size)))
}

func TestTriggerMemberClientForwardsExactScopeClearsMetadataAndBoundsCall(t *testing.T) {
	calls := 0
	client := &triggerTeamClient{rpc: triggerMemberTestRPC(func(ctx context.Context, req *userpb.ResolveTriggerTeamMemberRequest, options ...grpc.CallOption) (*userpb.ResolveTriggerTeamMemberResponse, error) {
		calls++
		md, ok := metadata.FromOutgoingContext(ctx)
		deadline, bounded := ctx.Deadline()
		if !ok || len(md) != 0 || !bounded || time.Until(deadline) > 2*time.Second || req.ActorId != 9007199254740993 || req.TeamId != 9007199254740995 || req.Name != "张三" {
			t.Fatalf("request=%v metadata=%v deadline=%v", req, md, deadline)
		}
		if len(options) != 1 {
			t.Fatalf("call options=%v", options)
		}
		option, ok := options[0].(grpc.MaxRecvMsgSizeCallOption)
		if !ok || option.MaxRecvMsgSize != model.AgentTriggerMemberResponseLimit {
			t.Fatalf("response limit option=%v", options[0])
		}
		return triggerAssigneeTestResponse(req.ActorId, req.TeamId, req.Name, 2), nil
	})}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer private-token", "cookie", "private", "actor-id", "1", "service", "agent", "extra", "secret"))
	result, err := client.Resolve(ctx, 9007199254740993, 9007199254740995, "张三")
	if err != nil || result == nil || len(result.Candidates) != 2 || calls != 1 {
		t.Fatalf("result=%v err=%v calls=%d", result, err, calls)
	}
}

func TestTriggerMemberClientRejectsEveryInvalidSuccessShape(t *testing.T) {
	cases := map[string]func(*userpb.ResolveTriggerTeamMemberResponse) *userpb.ResolveTriggerTeamMemberResponse{
		"nil": func(*userpb.ResolveTriggerTeamMemberResponse) *userpb.ResolveTriggerTeamMemberResponse { return nil },
		"actor": func(r *userpb.ResolveTriggerTeamMemberResponse) *userpb.ResolveTriggerTeamMemberResponse {
			r.ActorId++
			return r
		},
		"team": func(r *userpb.ResolveTriggerTeamMemberResponse) *userpb.ResolveTriggerTeamMemberResponse {
			r.TeamId++
			return r
		},
		"name": func(r *userpb.ResolveTriggerTeamMemberResponse) *userpb.ResolveTriggerTeamMemberResponse {
			r.Name = "李四"
			return r
		},
		"nil candidate": func(r *userpb.ResolveTriggerTeamMemberResponse) *userpb.ResolveTriggerTeamMemberResponse {
			r.Candidates[0] = nil
			return r
		},
		"zero ID": func(r *userpb.ResolveTriggerTeamMemberResponse) *userpb.ResolveTriggerTeamMemberResponse {
			r.Candidates[0].UserId = 0
			return r
		},
		"unmatched": func(r *userpb.ResolveTriggerTeamMemberResponse) *userpb.ResolveTriggerTeamMemberResponse {
			r.Candidates[0].Username = "李四"
			return r
		},
		"empty username": func(r *userpb.ResolveTriggerTeamMemberResponse) *userpb.ResolveTriggerTeamMemberResponse {
			r.Candidates[0].Username = ""
			r.Candidates[0].Nickname = "张三"
			return r
		},
		"invalid UTF8": func(r *userpb.ResolveTriggerTeamMemberResponse) *userpb.ResolveTriggerTeamMemberResponse {
			r.Candidates[0].Nickname = "\xff"
			return r
		},
		"long nickname": func(r *userpb.ResolveTriggerTeamMemberResponse) *userpb.ResolveTriggerTeamMemberResponse {
			r.Candidates[0].Nickname = strings.Repeat("三", 65)
			return r
		},
		"long username": func(r *userpb.ResolveTriggerTeamMemberResponse) *userpb.ResolveTriggerTeamMemberResponse {
			r.Candidates[0].Username = strings.Repeat("三", 65)
			r.Candidates[0].Nickname = "张三"
			return r
		},
		"duplicate": func(r *userpb.ResolveTriggerTeamMemberResponse) *userpb.ResolveTriggerTeamMemberResponse {
			r.Candidates[1].UserId = r.Candidates[0].UserId
			return r
		},
		"unordered": func(r *userpb.ResolveTriggerTeamMemberResponse) *userpb.ResolveTriggerTeamMemberResponse {
			r.Candidates[0], r.Candidates[1] = r.Candidates[1], r.Candidates[0]
			return r
		},
		"twenty one": func(r *userpb.ResolveTriggerTeamMemberResponse) *userpb.ResolveTriggerTeamMemberResponse {
			return triggerAssigneeTestResponse(r.ActorId, r.TeamId, r.Name, 21)
		},
		"false truncation": func(r *userpb.ResolveTriggerTeamMemberResponse) *userpb.ResolveTriggerTeamMemberResponse {
			r.Truncated = true
			return r
		},
		"oversize": func(r *userpb.ResolveTriggerTeamMemberResponse) *userpb.ResolveTriggerTeamMemberResponse {
			r.ProtoReflect().SetUnknown(triggerMemberTestPadding(model.AgentTriggerMemberResponseLimit))
			return r
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			calls := 0
			client := &triggerTeamClient{rpc: triggerMemberTestRPC(func(context.Context, *userpb.ResolveTriggerTeamMemberRequest, ...grpc.CallOption) (*userpb.ResolveTriggerTeamMemberResponse, error) {
				calls++
				return mutate(triggerAssigneeTestResponse(7, 9, "张三", 2)), nil
			})}
			result, err := client.Resolve(context.Background(), 7, 9, "张三")
			if result != nil || status.Code(err) != codes.Unavailable || calls != 1 {
				t.Fatalf("result=%v err=%v calls=%d", result, err, calls)
			}
		})
	}
	for _, count := range []int{0, 1, 2, 20} {
		response := triggerAssigneeTestResponse(7, 9, "张三", count)
		if count == 1 {
			response.Candidates[0].Username = "literal-login"
			response.Candidates[0].Nickname = "张三"
		}
		if !validTriggerMemberResponse(response, 7, 9, "张三") {
			t.Fatalf("valid %d candidates rejected", count)
		}
		if count == 20 {
			response.Truncated = true
			if !validTriggerMemberResponse(response, 7, 9, "张三") {
				t.Fatal("valid truncated candidates rejected")
			}
		}
	}
}

func TestTriggerMemberClientMapsErrorsSafelyWithoutRetryOrFallback(t *testing.T) {
	for _, upstream := range []error{status.Error(codes.PermissionDenied, "private"), status.Error(codes.Unauthenticated, "private"), status.Error(codes.InvalidArgument, "private"), status.Error(codes.Canceled, "private"), status.Error(codes.DeadlineExceeded, "private"), status.Error(codes.Unimplemented, "private"), status.Error(codes.Internal, "private"), errors.New("private"), context.Canceled, context.DeadlineExceeded} {
		calls := 0
		client := &triggerTeamClient{rpc: triggerMemberTestRPC(func(context.Context, *userpb.ResolveTriggerTeamMemberRequest, ...grpc.CallOption) (*userpb.ResolveTriggerTeamMemberResponse, error) {
			calls++
			return nil, upstream
		})}
		result, err := client.Resolve(context.Background(), 7, 9, "张三")
		want := status.Code(upstream)
		if errors.Is(upstream, context.Canceled) {
			want = codes.Canceled
		} else if errors.Is(upstream, context.DeadlineExceeded) {
			want = codes.DeadlineExceeded
		}
		switch want {
		case codes.PermissionDenied, codes.Unauthenticated, codes.InvalidArgument, codes.Canceled, codes.DeadlineExceeded:
		default:
			want = codes.Unavailable
		}
		if result != nil || status.Code(err) != want || calls != 1 || strings.Contains(err.Error(), "private") {
			t.Fatalf("upstream=%v result=%v err=%v calls=%d", upstream, result, err, calls)
		}
	}
}

func TestTriggerMemberClientLocalGuardsAndCancellationNeverLeakResults(t *testing.T) {
	client := &triggerTeamClient{rpc: triggerMemberTestRPC(func(context.Context, *userpb.ResolveTriggerTeamMemberRequest, ...grpc.CallOption) (*userpb.ResolveTriggerTeamMemberResponse, error) {
		t.Fatal("invalid local request reached RPC")
		return nil, nil
	})}
	for _, scope := range [][2]int64{{0, 1}, {1, 0}, {-1, 1}, {1, -1}} {
		if result, err := client.Resolve(context.Background(), scope[0], scope[1], "张三"); result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("result=%v err=%v", result, err)
		}
	}
	for _, name := range []string{"", " 张三", "张三 ", "\xff", strings.Repeat("张", 65)} {
		if result, err := client.Resolve(context.Background(), 7, 9, name); result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("result=%v err=%v", result, err)
		}
	}
	if _, err := client.Resolve(nil, 7, 9, "张三"); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	var absent *triggerTeamClient
	if _, err := absent.Resolve(context.Background(), 7, 9, "张三"); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	if _, err := (&triggerTeamClient{}).Resolve(context.Background(), 7, 9, "张三"); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := absent.Resolve(ctx, 0, 0, ""); status.Code(err) != codes.Canceled {
		t.Fatal(err)
	}
	for _, valid := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		client := &triggerTeamClient{rpc: triggerMemberTestRPC(func(context.Context, *userpb.ResolveTriggerTeamMemberRequest, ...grpc.CallOption) (*userpb.ResolveTriggerTeamMemberResponse, error) {
			cancel()
			if valid {
				return triggerAssigneeTestResponse(7, 9, "张三", 1), nil
			}
			return nil, errors.New("private")
		})}
		if result, err := client.Resolve(ctx, 7, 9, "张三"); result != nil || status.Code(err) != codes.Canceled {
			t.Fatalf("result=%v err=%v", result, err)
		}
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	client = &triggerTeamClient{rpc: triggerMemberTestRPC(func(ctx context.Context, _ *userpb.ResolveTriggerTeamMemberRequest, _ ...grpc.CallOption) (*userpb.ResolveTriggerTeamMemberResponse, error) {
		<-ctx.Done()
		return nil, errors.New("private")
	})}
	if result, err := client.Resolve(ctx, 7, 9, "张三"); result != nil || status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("result=%v err=%v", result, err)
	}
}

type triggerMemberTestServer struct {
	userpb.UnimplementedUserTriggerServer
	resolve func(context.Context, *userpb.ResolveTriggerTeamMemberRequest) (*userpb.ResolveTriggerTeamMemberResponse, error)
	check   func(context.Context, *userpb.CheckTriggerTeamMemberRequest) (*userpb.CheckTriggerTeamMemberResponse, error)
}

func (s *triggerMemberTestServer) ResolveTriggerTeamMember(ctx context.Context, req *userpb.ResolveTriggerTeamMemberRequest) (*userpb.ResolveTriggerTeamMemberResponse, error) {
	return s.resolve(ctx, req)
}
func (s *triggerMemberTestServer) CheckTriggerTeamMember(ctx context.Context, req *userpb.CheckTriggerTeamMemberRequest) (*userpb.CheckTriggerTeamMemberResponse, error) {
	if s.check == nil {
		return nil, status.Error(codes.Unimplemented, "unused")
	}
	return s.check(ctx, req)
}

func startTriggerMemberTestServer(t *testing.T, files rpcauth.CertificateFiles, service *triggerMemberTestServer) string {
	t.Helper()
	creds, err := rpcauth.NewServiceServerCredentials(files, "im.go-im.internal")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(creds))
	userpb.RegisterUserTriggerServer(server, service)
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close(); <-done })
	return listener.Addr().String()
}

func TestTriggerMemberClientActualMTLSLimitsOnlyResolveAndPreservesOldCheckBudget(t *testing.T) {
	files := triggerClientCertificates(t)
	var calls, checks atomic.Int32
	service := &triggerMemberTestServer{resolve: func(ctx context.Context, req *userpb.ResolveTriggerTeamMemberRequest) (*userpb.ResolveTriggerTeamMemberResponse, error) {
		if err := rpcauth.RequireServiceIdentity(ctx, "im.go-im.internal"); err != nil {
			return nil, err
		}
		md, _ := metadata.FromIncomingContext(ctx)
		for _, key := range []string{"authorization", "cookie", "actor-id", "extra"} {
			if len(md.Get(key)) != 0 {
				t.Errorf("metadata leaked: %s", key)
			}
		}
		if req.ActorId != 9007199254740993 || req.TeamId != 9007199254740995 || req.Name != "张三" {
			t.Error("request scope/name changed")
		}
		number := calls.Add(1)
		response := triggerAssigneeTestResponse(req.ActorId, req.TeamId, req.Name, 1)
		if number == 1 {
			response.ProtoReflect().SetUnknown(triggerMemberTestPadding(8 * 1024))
			return response, nil
		}
		if number == 2 {
			response.ProtoReflect().SetUnknown(triggerMemberTestPadding(33 * 1024))
			return response, nil
		}
		return nil, status.Error(codes.Unimplemented, "private old User deployment")
	}, check: func(_ context.Context, req *userpb.CheckTriggerTeamMemberRequest) (*userpb.CheckTriggerTeamMemberResponse, error) {
		checks.Add(1)
		response := &userpb.CheckTriggerTeamMemberResponse{ActorId: req.ActorId, TeamId: req.TeamId}
		response.ProtoReflect().SetUnknown(triggerMemberTestPadding(8 * 1024))
		return response, nil
	}}
	address := startTriggerMemberTestServer(t, files["user.go-im.internal"], service)
	client, err := newTriggerTeamClient(triggerTeamClientConfig{Addr: address, ServerDNSName: "user.go-im.internal", Files: files["im.go-im.internal"]})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer private", "cookie", "private", "actor-id", "1", "extra", "secret")), 5*time.Second)
	defer cancel()
	if response, err := client.Resolve(ctx, 9007199254740993, 9007199254740995, "张三"); err != nil || response == nil {
		t.Fatalf("valid >4KiB Resolve rejected: %v %v", response, err)
	}
	if err := client.Check(ctx, 9007199254740993, 9007199254740995); status.Code(err) != codes.Unavailable {
		t.Fatalf("old Check accepted >4KiB: %v", err)
	}
	if response, err := client.Resolve(ctx, 9007199254740993, 9007199254740995, "张三"); response != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("oversized Resolve accepted: %v %v", response, err)
	}
	if response, err := client.Resolve(ctx, 9007199254740993, 9007199254740995, "张三"); response != nil || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") {
		t.Fatalf("legacy deployment fallback: %v %v", response, err)
	}
	if calls.Load() != 3 || checks.Load() != 1 {
		t.Fatalf("unexpected retry/fallback: Resolve=%d Check=%d", calls.Load(), checks.Load())
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if response, err := client.Resolve(context.Background(), 7, 9, "张三"); response != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("closed client: %v %v", response, err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := client.Resolve(cancelled, 7, 9, "张三"); status.Code(err) != codes.Canceled {
		t.Fatalf("closed guard hid cancellation: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("closed connection made RPC: %d", calls.Load())
	}
}
