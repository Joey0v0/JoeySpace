package agent

import (
	"context"

	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type draftIdentityClient interface {
	GetMyInfo(context.Context, *userpb.GetMyInfoRequest, ...grpc.CallOption) (*userpb.GetUserInfoResponse, error)
}

type draftIdentityResolver struct{ users draftIdentityClient }

// currentUserID resolves the original Token through the user service; callers
// must never use a user ID supplied by the browser or model for draft access.
func (r *draftIdentityResolver) currentUserID(ctx context.Context, token string) (int64, error) {
	if r == nil || r.users == nil {
		return 0, status.Error(codes.Unavailable, "user identity check is not configured")
	}
	readCtx, cancel, err := authorizedReadContext(ctx, token)
	if err != nil {
		return 0, err
	}
	defer cancel()
	user, err := r.users.GetMyInfo(readCtx, &userpb.GetMyInfoRequest{})
	if readCtx.Err() != nil {
		return 0, status.FromContextError(readCtx.Err()).Err()
	}
	if err != nil {
		switch status.Code(err) {
		case codes.Unauthenticated, codes.PermissionDenied, codes.NotFound:
			return 0, err
		default:
			return 0, status.Error(codes.Unavailable, "user identity check unavailable")
		}
	}
	if user.GetId() <= 0 {
		return 0, status.Error(codes.Unavailable, "user identity response invalid")
	}
	return user.GetId(), nil
}
