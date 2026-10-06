package push

import (
	"context"
	"errors"
	"testing"

	"github.com/yjydist/go-im/internal/rpcauth"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type fakeUserPushRPC struct {
	response *userpb.CheckPushTeamMemberResponse
	err      error
	request  *userpb.CheckPushTeamMemberRequest
	metadata metadata.MD
}

func (f *fakeUserPushRPC) CheckPushTeamMember(ctx context.Context, req *userpb.CheckPushTeamMemberRequest, _ ...grpc.CallOption) (*userpb.CheckPushTeamMemberResponse, error) {
	f.request = req
	f.metadata, _ = metadata.FromOutgoingContext(ctx)
	return f.response, f.err
}

func TestTeamEligibilityClientClassifiesOnlyExplicitDenialAsSkip(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *userpb.CheckPushTeamMemberResponse
		rpcErr   error
		allowed  bool
		wantCode codes.Code
	}{
		{"active", &userpb.CheckPushTeamMemberResponse{TeamId: 100, UserId: 42, Generation: 7}, nil, true, codes.OK},
		{"left", nil, status.Error(codes.PermissionDenied, "left"), false, codes.OK},
		{"User unavailable", nil, status.Error(codes.Unavailable, "private"), false, codes.Unavailable},
		{"unauthenticated", nil, status.Error(codes.Unauthenticated, "private"), false, codes.Unavailable},
		{"bad team", &userpb.CheckPushTeamMemberResponse{TeamId: 101, UserId: 42, Generation: 7}, nil, false, codes.Unavailable},
		{"bad user", &userpb.CheckPushTeamMemberResponse{TeamId: 100, UserId: 41, Generation: 7}, nil, false, codes.Unavailable},
		{"missing generation", &userpb.CheckPushTeamMemberResponse{TeamId: 100, UserId: 42}, nil, false, codes.Unavailable},
		{"nil response", nil, nil, false, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rpc := &fakeUserPushRPC{response: tc.response, err: tc.rpcErr}
			client := &TeamEligibilityClient{rpc: rpc}
			ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer do-not-forward"))
			generation, allowed, err := client.CheckCurrentTeamMember(ctx, 100, 42)
			if status.Code(err) != tc.wantCode || allowed != tc.allowed || allowed && generation != 7 || rpc.request.GetTeamId() != 100 || rpc.request.GetUserId() != 42 || len(rpc.metadata) != 0 {
				t.Fatalf("generation=%d allowed=%t err=%v request=%v metadata=%v", generation, allowed, err, rpc.request, rpc.metadata)
			}
		})
	}
}

func TestTeamEligibilityClientConfigurationAndInvalidCalls(t *testing.T) {
	if c, err := LoadTeamEligibilityClientConfig(func(string) string { return "" }); err != nil || c.Addr != "" {
		t.Fatalf("disabled=%+v %v", c, err)
	}
	if _, err := LoadTeamEligibilityClientConfig(nil); err == nil {
		t.Fatal("nil reader accepted")
	}
	if _, err := LoadTeamEligibilityClientConfig(func(k string) string {
		if k == "PUSH_USER_RPC_ADDR" {
			return "user:9007"
		}
		return ""
	}); err == nil {
		t.Fatal("partial config accepted")
	}
	for _, addr := range []string{"user", "user:0", "user:65536", "*:9007", "user:9007 ", "https://user:9007"} {
		if _, err := validateTeamEligibilityClientConfig(TeamEligibilityClientConfig{Addr: addr, Files: structFiles()}); err == nil {
			t.Fatalf("address %q accepted", addr)
		}
	}
	if client, err := NewTeamEligibilityClient(TeamEligibilityClientConfig{}); client != nil || err != nil {
		t.Fatalf("disabled client=%v %v", client, err)
	}
	var absent *TeamEligibilityClient
	if _, _, err := absent.CheckCurrentTeamMember(context.Background(), 100, 42); status.Code(err) != codes.Unavailable {
		t.Fatalf("nil client=%v", err)
	}
	if _, _, err := (&TeamEligibilityClient{rpc: &fakeUserPushRPC{}}).CheckCurrentTeamMember(context.Background(), 0, 42); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid scope=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := (&TeamEligibilityClient{rpc: &fakeUserPushRPC{}}).CheckCurrentTeamMember(ctx, 100, 42); !errors.Is(err, context.Canceled) && status.Code(err) != codes.Canceled {
		t.Fatalf("canceled=%v", err)
	}
}

func structFiles() rpcauth.CertificateFiles {
	return rpcauth.CertificateFiles{CertFile: "client.pem", KeyFile: "client.key", CAFile: "ca.pem"}
}
