package main

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxResolvedTeamMembers = 20

func (s *userServer) ResolveTeamMember(ctx context.Context, req *pb.ResolveTeamMemberRequest) (*pb.ResolveTeamMemberResponse, error) {
	if s.db == nil {
		return nil, status.Error(codes.Unavailable, "team database is not enabled")
	}
	name := strings.TrimSpace(req.GetName())
	if req.GetTeamId() <= 0 || !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 64 {
		return nil, status.Error(codes.InvalidArgument, "invalid team or member name")
	}
	if _, err := s.CheckTeamMember(ctx, &pb.CheckTeamMemberRequest{TeamId: req.GetTeamId()}); err != nil {
		return nil, err
	}

	var rows []struct {
		UserID   int64
		Username string
		Nickname string
		Role     int8
	}
	// Compare bytes explicitly: the default Unicode collation also equates some
	// different cases/accents. One OR query preserves cross-field ambiguity and
	// returns each membership once, without preferring usernames over nicknames.
	err := s.db.WithContext(ctx).Table("team_members").
		Select("team_members.user_id AS user_id, users.username, users.nickname, team_members.role").
		Joins("JOIN users ON users.id = team_members.user_id").
		Where("team_members.team_id = ? AND users.status = ?", req.GetTeamId(), 1).
		Where("(CAST(users.username AS BINARY) = CAST(? AS BINARY) OR CAST(users.nickname AS BINARY) = CAST(? AS BINARY))", name, name).
		Order("team_members.user_id ASC").Limit(maxResolvedTeamMembers + 1).Find(&rows).Error
	if err != nil {
		return nil, teamMemberDBError(ctx, err)
	}
	result := &pb.ResolveTeamMemberResponse{
		Candidates: make([]*pb.TeamMember, 0, min(len(rows), maxResolvedTeamMembers)),
		Truncated:  len(rows) > maxResolvedTeamMembers,
	}
	if result.Truncated {
		rows = rows[:maxResolvedTeamMembers]
	}
	for _, row := range rows {
		result.Candidates = append(result.Candidates, &pb.TeamMember{
			UserId: row.UserID, Username: row.Username, Nickname: row.Nickname, Role: int32(row.Role),
		})
	}
	return result, nil
}
