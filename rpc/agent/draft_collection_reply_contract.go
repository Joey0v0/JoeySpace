package agent

import (
	"context"

	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
)

// Each item owns an immutable card from an already committed Task result.
// Implementations never call a remote service while holding the draft lock.
type draftCollectionReplyStore interface {
	prepareCollectionReply(context.Context, taskDraftCollection, int32) (draftReplyRecord, error)
	loadCollectionReply(context.Context, taskDraftCollection, int32) (draftReplyRecord, bool, error)
	acceptCollectionReply(context.Context, draftReplyRecord) error
}

type draftItemBotClient interface {
	PostTaskCreatedCardItem(context.Context, *impb.PostTaskCreatedCardItemRequest, ...grpc.CallOption) (*impb.PostTaskCreatedCardResponse, error)
}
