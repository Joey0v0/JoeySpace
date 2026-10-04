package main

import (
	"context"

	"github.com/yjydist/go-im/rpc/im/pb"
	"gorm.io/gorm"
)

type triggerTeamEligibility interface {
	Check(context.Context, int64, int64) error
}

// Unlike ordinary IM, this read-only handler derives its actor from IM storage.
type triggerContextServer struct {
	pb.UnimplementedIMTriggerServer
	db           *gorm.DB
	teams        triggerTeamEligibility
	agentDNSName string
}
