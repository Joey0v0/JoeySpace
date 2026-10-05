package handler

import (
	"context"

	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
)

// OfflineMessagesClient keeps the legacy HTTP adapter limited to IM-owned
// delivery reads and explicit acknowledgements. It has no local DB fallback.
type OfflineMessagesClient interface {
	ListOfflineMessages(context.Context, *pb.ListOfflineMessagesRequest, ...grpc.CallOption) (*pb.ListOfflineMessagesResponse, error)
	AckOfflineMessages(context.Context, *pb.AckOfflineMessagesRequest, ...grpc.CallOption) (*pb.AckOfflineMessagesResponse, error)
}
