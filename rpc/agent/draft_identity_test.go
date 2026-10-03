package agent

import (
	"context"
	"errors"
	"testing"

	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type draftUserFunc func(context.Context) (*userpb.GetUserInfoResponse, error)

func (f draftUserFunc) GetMyInfo(ctx context.Context, _ *userpb.GetMyInfoRequest, _ ...grpc.CallOption) (*userpb.GetUserInfoResponse, error) {
	return f(ctx)
}

func TestDraftIdentityUsesOriginalTokenAndVerifiedUserID(t *testing.T) {
	resolver := &draftIdentityResolver{users: draftUserFunc(func(ctx context.Context) (*userpb.GetUserInfoResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if values := md.Get("authorization"); len(values) != 1 || values[0] != "Bearer user-token" {
			t.Fatalf("authorization = %v", values)
		}
		return &userpb.GetUserInfoResponse{Id: 9007199254740993}, nil
	})}
	actorID, err := resolver.currentUserID(context.Background(), "user-token")
	if err != nil || actorID != 9007199254740993 {
		t.Fatalf("actor ID = %d, %v", actorID, err)
	}
}

func TestDraftIdentityRejectsMissingOrInvalidIdentity(t *testing.T) {
	if _, err := (&draftIdentityResolver{}).currentUserID(context.Background(), "user-token"); status.Code(err) != codes.Unavailable {
		t.Fatalf("missing client error = %v", err)
	}
	resolver := &draftIdentityResolver{users: draftUserFunc(func(context.Context) (*userpb.GetUserInfoResponse, error) {
		t.Fatal("invalid token reached user service")
		return nil, nil
	})}
	for _, token := range []string{"", "bad token"} {
		if _, err := resolver.currentUserID(context.Background(), token); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("token %q error = %v", token, err)
		}
	}
	resolver.users = draftUserFunc(func(context.Context) (*userpb.GetUserInfoResponse, error) {
		return &userpb.GetUserInfoResponse{}, nil
	})
	if _, err := resolver.currentUserID(context.Background(), "user-token"); status.Code(err) != codes.Unavailable {
		t.Fatalf("empty identity error = %v", err)
	}
}

func TestDraftIdentityPreservesDenialAndMasksBackendFailure(t *testing.T) {
	for _, code := range []codes.Code{codes.Unauthenticated, codes.PermissionDenied, codes.NotFound} {
		resolver := &draftIdentityResolver{users: draftUserFunc(func(context.Context) (*userpb.GetUserInfoResponse, error) {
			return nil, status.Error(code, "identity rejected")
		})}
		if _, err := resolver.currentUserID(context.Background(), "user-token"); status.Code(err) != code {
			t.Fatalf("%v error = %v", code, err)
		}
	}
	resolver := &draftIdentityResolver{users: draftUserFunc(func(context.Context) (*userpb.GetUserInfoResponse, error) {
		return nil, errors.New("private identity database detail")
	})}
	if _, err := resolver.currentUserID(context.Background(), "user-token"); status.Code(err) != codes.Unavailable {
		t.Fatalf("backend error = %v", err)
	}
}
