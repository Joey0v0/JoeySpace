package handler

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Only the caller's single Bearer credential crosses this legacy adapter.
// Replacing outgoing metadata prevents forwarding a caller-supplied identity or
// service credential accidentally stored on the HTTP request context.
func offlineRPCContext(c *gin.Context) (context.Context, context.CancelFunc, bool) {
	headers := c.Request.Header.Values("Authorization")
	if len(headers) != 1 {
		return nil, nil, false
	}
	parts := strings.Split(headers[0], " ")
	if len(parts) != 2 || parts[0] != "Bearer" || parts[1] == "" || strings.ContainsAny(parts[1], "\t\r\n,") {
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", headers[0])), cancel, true
}

func offlineRPCErrorCode(err error) int {
	switch status.Code(err) {
	case codes.InvalidArgument:
		return errcode.ErrBadRequest
	case codes.Unauthenticated:
		return errcode.ErrUnAuth
	case codes.PermissionDenied:
		return errcode.ErrForbidden
	default:
		return errcode.ErrInternal
	}
}

// Validate the full page before writing any message content. Keep the legacy
// sender_type=0 user compatibility but reject malformed identities and enums.
func validOfflineMessage(message *pb.OfflineMessage) bool {
	if message == nil || message.Id <= 0 || message.MsgId == "" || message.FromId <= 0 || message.ToId <= 0 || message.CreatedAt == nil || message.CreatedAt.CheckValid() != nil {
		return false
	}
	if (message.ChatType != 1 && message.ChatType != 2) || message.ContentType < 1 || message.ContentType > 4 {
		return false
	}
	switch message.SenderType {
	case 0, 1:
		return message.InitiatorId == 0
	case 2:
		return message.InitiatorId > 0
	default:
		return false
	}
}
