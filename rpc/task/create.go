package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/snowflake"
	"github.com/go-sql-driver/mysql"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/core/logx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

type teamChecker interface {
	CheckTeamMember(context.Context, *userpb.CheckTeamMemberRequest, ...grpc.CallOption) (*userpb.CheckTeamMemberResponse, error)
	CheckTeamMemberByID(context.Context, *userpb.CheckTeamMemberByIDRequest, ...grpc.CallOption) (*userpb.CheckTeamMemberByIDResponse, error)
}

const maxTaskDueAtUnixMs int64 = 253402300799999 // 9999-12-31 23:59:59.999 UTC

type taskServer struct {
	pb.UnimplementedTaskServer
	db             *gorm.DB
	idNode         *snowflake.Node
	teamClient     teamChecker
	identityClient interface {
		GetMyInfo(context.Context, *userpb.GetMyInfoRequest, ...grpc.CallOption) (*userpb.GetUserInfoResponse, error)
	}
	teamDirectory interface {
		ListMyTeams(context.Context, *userpb.ListMyTeamsRequest, ...grpc.CallOption) (*userpb.ListMyTeamsResponse, error)
	}
	now      func() time.Time
	imClient interface {
		CheckTeamGroupMessage(context.Context, *impb.CheckTeamGroupMessageRequest, ...grpc.CallOption) (*impb.CheckTeamGroupMessageResponse, error)
	}
}

func (s *taskServer) CreateTask(ctx context.Context, req *pb.CreateTaskRequest) (*pb.CreateTaskResponse, error) {
	if s.db == nil || s.idNode == nil || s.teamClient == nil {
		return nil, status.Error(codes.Unavailable, "task creation is not enabled")
	}
	title, description := strings.TrimSpace(req.GetTitle()), strings.TrimSpace(req.GetDescription())
	if req.GetTeamId() <= 0 || req.GetAssigneeId() < 0 || req.GetSourceGroupId() < 0 || req.GetSourceMessageId() < 0 || (req.GetSourceGroupId() == 0) != (req.GetSourceMessageId() == 0) || req.GetDueAtUnixMs() < 0 || req.GetDueAtUnixMs() > maxTaskDueAtUnixMs || !utf8.ValidString(title) || utf8.RuneCountInString(title) < 1 || utf8.RuneCountInString(title) > 200 || !utf8.ValidString(description) || utf8.RuneCountInString(description) > 2000 {
		return nil, status.Error(codes.InvalidArgument, "invalid task fields")
	}
	teamCtx, err := taskTeamContext(ctx)
	if err != nil {
		return nil, err
	}
	md, _ := metadata.FromIncomingContext(ctx)
	keys := md.Get("idempotency-key")
	if len(keys) != 1 || !validTaskRequestKey(keys[0]) {
		return nil, status.Error(codes.InvalidArgument, "invalid idempotency key")
	}
	member, err := s.currentTeamMember(teamCtx, req.GetTeamId())
	if err != nil {
		return nil, err
	}
	fingerprint, err := taskRequestFingerprint(req.GetTeamId(), title, description, req.GetAssigneeId(), req.GetSourceGroupId(), req.GetSourceMessageId(), req.GetDueAtUnixMs())
	if err != nil {
		return nil, status.Error(codes.Internal, "cannot encode task request")
	}
	previous, err := s.findTaskRequest(ctx, member.GetUserId(), keys[0])
	if err == nil {
		return taskRequestResult(previous, fingerprint)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, taskDatabaseError(ctx, err)
	}
	if req.GetAssigneeId() > 0 {
		if _, err := s.teamClient.CheckTeamMemberByID(teamCtx, &userpb.CheckTeamMemberByIDRequest{TeamId: req.GetTeamId(), UserId: req.GetAssigneeId()}); err != nil {
			return nil, taskTeamError(err)
		}
	}
	if req.GetSourceGroupId() > 0 {
		if s.imClient == nil {
			return nil, status.Error(codes.Unavailable, "IM message check is not enabled")
		}
		if _, err := s.imClient.CheckTeamGroupMessage(teamCtx, &impb.CheckTeamGroupMessageRequest{
			TeamId: req.GetTeamId(), GroupId: req.GetSourceGroupId(), MessageId: req.GetSourceMessageId(),
		}); err != nil {
			switch status.Code(err) {
			case codes.Unauthenticated, codes.PermissionDenied, codes.NotFound, codes.DeadlineExceeded, codes.Canceled:
				return nil, err
			default:
				return nil, status.Error(codes.Unavailable, "IM message check unavailable")
			}
		}
	}

	var assignee *int64
	if req.GetAssigneeId() > 0 {
		id := req.GetAssigneeId()
		assignee = &id
	}
	var sourceGroupID, sourceMessageID *int64
	if req.GetSourceGroupId() > 0 {
		groupID, messageID := req.GetSourceGroupId(), req.GetSourceMessageId()
		sourceGroupID, sourceMessageID = &groupID, &messageID
	}
	var dueAtUnixMs *int64
	if req.GetDueAtUnixMs() > 0 {
		dueAt := req.GetDueAtUnixMs()
		dueAtUnixMs = &dueAt
	}
	taskID := s.idNode.Generate().Int64()
	task := struct {
		ID              int64
		TeamID          int64
		Title           string
		Description     string
		CreatorID       int64
		AssigneeID      *int64
		SourceGroupID   *int64
		SourceMessageID *int64
		DueAtUnixMs     *int64
		Status          int8
		RequestKey      string
		RequestHash     string
	}{taskID, req.GetTeamId(), title, description, member.GetUserId(), assignee, sourceGroupID, sourceMessageID, dueAtUnixMs, 0, keys[0], fingerprint}
	if err := s.db.WithContext(ctx).Table("tasks").Create(&task).Error; err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			previous, findErr := s.findTaskRequest(ctx, member.GetUserId(), keys[0])
			if findErr == nil {
				return taskRequestResult(previous, fingerprint)
			}
			err = findErr
		}
		return nil, taskDatabaseError(ctx, err)
	}
	return &pb.CreateTaskResponse{TaskId: taskID}, nil
}

type taskRequestRow struct {
	ID          int64
	RequestHash string
}

func (s *taskServer) findTaskRequest(ctx context.Context, creatorID int64, key string) (taskRequestRow, error) {
	var previous taskRequestRow
	err := s.db.WithContext(ctx).Table("tasks").Select("id, request_hash").
		Where("creator_id = ? AND request_key = ?", creatorID, key).Take(&previous).Error
	return previous, err
}

func taskRequestResult(previous taskRequestRow, fingerprint string) (*pb.CreateTaskResponse, error) {
	if previous.RequestHash != fingerprint {
		return nil, status.Error(codes.AlreadyExists, "idempotency key used for another task request")
	}
	return &pb.CreateTaskResponse{TaskId: previous.ID}, nil
}

func taskDatabaseError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	logx.WithContext(ctx).Errorf("task database failed: %v", err)
	return status.Error(codes.Unavailable, "task database unavailable")
}

func taskRequestFingerprint(teamID int64, title, description string, assigneeID, sourceGroupID, sourceMessageID, dueAtUnixMs int64) (string, error) {
	data, err := json.Marshal(struct {
		TeamID          int64  `json:"team_id"`
		Title           string `json:"title"`
		Description     string `json:"description"`
		AssigneeID      int64  `json:"assignee_id"`
		SourceGroupID   int64  `json:"source_group_id,omitempty"`
		SourceMessageID int64  `json:"source_message_id,omitempty"`
		DueAtUnixMs     int64  `json:"due_at_unix_ms,omitempty"`
	}{teamID, title, description, assigneeID, sourceGroupID, sourceMessageID, dueAtUnixMs})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func taskTeamError(err error) error {
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied, codes.NotFound, codes.FailedPrecondition, codes.DeadlineExceeded, codes.Canceled:
		return err
	default:
		return status.Error(codes.Unavailable, "team membership check unavailable")
	}
}

func taskTeamContext(ctx context.Context) (context.Context, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	authorizations := md.Get("authorization")
	if len(authorizations) != 1 || authorizations[0] == "" {
		return nil, status.Error(codes.Unauthenticated, "login token required")
	}
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", authorizations[0])), nil
}

func (s *taskServer) currentTeamMember(teamCtx context.Context, teamID int64) (*userpb.CheckTeamMemberResponse, error) {
	member, err := s.teamClient.CheckTeamMember(teamCtx, &userpb.CheckTeamMemberRequest{TeamId: teamID})
	if err != nil {
		return nil, taskTeamError(err)
	}
	if member.GetUserId() <= 0 {
		return nil, status.Error(codes.Unavailable, "team membership response invalid")
	}
	return member, nil
}

func validTaskRequestKey(key string) bool {
	if len(key) < 1 || len(key) > 64 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
