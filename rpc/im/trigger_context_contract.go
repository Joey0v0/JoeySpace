package main

import (
	"context"

	"github.com/yjydist/go-im/rpc/im/pb"
	"gorm.io/gorm"
)

type triggerTeamEligibility interface {
	Check(context.Context, int64, int64) error
	// CheckGeneration returns User's current positive active membership
	// generation for the saved actor/team; no caller JWT is delegated.
	CheckGeneration(context.Context, int64, int64) (int64, error)
}

// Unlike ordinary IM, this read-only handler derives its actor from IM storage.
type triggerContextServer struct {
	pb.UnimplementedIMTriggerServer
	db           *gorm.DB
	teams        triggerTeamEligibility
	agentDNSName string
}
