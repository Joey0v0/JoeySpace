package main

import (
	"context"

	userpb "github.com/yjydist/go-im/rpc/user/pb"
)

// An optional capability of the existing dedicated IM->User connection.
// Keeping Check separate preserves existing context-only test implementations.
type triggerMemberResolver interface {
	Resolve(context.Context, int64, int64, string) (*userpb.ResolveTriggerTeamMemberResponse, error)
}
