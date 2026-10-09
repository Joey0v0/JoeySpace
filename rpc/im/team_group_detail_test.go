package main

import (
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"regexp"
	"testing"
)

func TestGetTeamGroupDiscovery(t *testing.T) {
	for _, joined := range []bool{false, true} {
		t.Run(map[bool]string{false: "not joined", true: "joined"}[joined], func(t *testing.T) {
			s, m := testIMServer(t)
			s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
			m.ExpectQuery(regexp.QuoteMeta("SELECT id, name, owner_id FROM `groups` WHERE id = ? AND team_id = ? LIMIT ?")).WithArgs(int64(100), int64(200), 1).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "owner_id"}).AddRow(100, "Group", 42))
			rows := sqlmock.NewRows([]string{"group_id", "closed_through_generation"})
			if joined {
				rows.AddRow(100, 0)
			}
			m.ExpectQuery(regexp.QuoteMeta(teamGroupReadFenceSQL)).WithArgs(int64(100), int64(42), int64(200)).WillReturnRows(rows)
			got, err := s.GetTeamGroup(teamGroupListContext(t), &pb.GetTeamGroupRequest{TeamId: 200, GroupId: 100})
			if err != nil || got.GetGroup().GetJoined() != joined {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}
func TestGetTeamGroupUnknownAndForeign(t *testing.T) {
	s, m := testIMServer(t)
	s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
	m.ExpectQuery("SELECT id, name, owner_id FROM").WithArgs(int64(100), int64(200), 1).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "owner_id"}))
	got, err := s.GetTeamGroup(teamGroupListContext(t), &pb.GetTeamGroupRequest{TeamId: 200, GroupId: 100})
	if got != nil || status.Code(err) != codes.NotFound {
		t.Fatalf("%v %v", got, err)
	}
}

type directoryTeamClient struct {
	teamCheckFunc
	generation func() int64
}

func (c directoryTeamClient) CheckTeamMember(ctx context.Context, req *userpb.CheckTeamMemberRequest, _ ...grpc.CallOption) (*userpb.CheckTeamMemberResponse, error) {
	return &userpb.CheckTeamMemberResponse{UserId: 42, Generation: c.generation()}, nil
}
func TestGetTeamGroupClosedAndGenerationChange(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(map[bool]string{false: "closed", true: "rejoined during query"}[change], func(t *testing.T) {
			s, m := testIMServer(t)
			calls := 0
			s.teamClient = directoryTeamClient{generation: func() int64 {
				calls++
				if change && calls > 1 {
					return 2
				}
				return 1
			}}
			m.ExpectQuery("SELECT id, name, owner_id FROM").WithArgs(int64(100), int64(200), 1).WillReturnRows(sqlmock.NewRows([]string{"id", "name", "owner_id"}).AddRow(100, "Group", 42))
			m.ExpectQuery(regexp.QuoteMeta(teamGroupReadFenceSQL)).WithArgs(int64(100), int64(42), int64(200)).WillReturnRows(sqlmock.NewRows([]string{"group_id", "closed_through_generation"}).AddRow(100, 1))
			got, err := s.GetTeamGroup(teamGroupListContext(t), &pb.GetTeamGroupRequest{TeamId: 200, GroupId: 100})
			if change {
				if got != nil || status.Code(err) != codes.PermissionDenied {
					t.Fatalf("%v %v", got, err)
				}
			} else if err != nil || got.GetGroup().GetJoined() {
				t.Fatalf("%v %v", got, err)
			}
		})
	}
}
