package main

import (
	"github.com/yjydist/go-im/rpc/user/pb"
	"gorm.io/gorm"
)

// Distinct from the ordinary JWT User service; only registered on its mTLS port.
type triggerTeamServer struct {
	pb.UnimplementedUserTriggerServer
	db        *gorm.DB
	imDNSName string
}
