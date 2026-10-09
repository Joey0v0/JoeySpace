package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type taskNamesClient interface {
	BatchGetMyTeamNames(context.Context, *userpb.BatchGetMyTeamNamesRequest, ...grpc.CallOption) (*userpb.BatchGetMyTeamNamesResponse, error)
	BatchGetTeamMemberDisplayNames(context.Context, *userpb.BatchGetTeamMemberDisplayNamesRequest, ...grpc.CallOption) (*userpb.BatchGetTeamMemberDisplayNamesResponse, error)
}

type taskReadResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data,omitempty"`
}

type enrichedTaskItem struct {
	TaskID          int64  `json:"task_id,string"`
	TeamID          int64  `json:"team_id,string"`
	TeamName        string `json:"team_name"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	CreatorID       int64  `json:"creator_id,string"`
	CreatorName     string `json:"creator_name"`
	AssigneeID      int64  `json:"assignee_id,string"`
	AssigneeName    string `json:"assignee_name"`
	Status          int32  `json:"status"`
	SourceGroupID   int64  `json:"source_group_id,string"`
	SourceMessageID int64  `json:"source_message_id,string"`
	DueAtUnixMs     int64  `json:"due_at_unix_ms,string"`
}

func taskReadAuth(w http.ResponseWriter, r *http.Request) (context.Context, bool) {
	headers := r.Header.Values("Authorization")
	if len(headers) == 1 {
		parts := strings.Fields(headers[0])
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return metadata.NewOutgoingContext(r.Context(), metadata.Pairs("authorization", "Bearer "+parts[1])), true
		}
	}
	httpx.WriteJson(w, http.StatusUnauthorized, taskReadResponse{Code: errcode.ErrUnAuth, Msg: "login required"})
	return nil, false
}

func taskReadDecimal(value string) (int64, error) {
	if value == "" {
		return 0, status.Error(codes.InvalidArgument, "invalid decimal ID")
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, status.Error(codes.InvalidArgument, "invalid decimal ID")
		}
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, status.Error(codes.InvalidArgument, "invalid decimal ID")
	}
	return id, nil
}

func writeTaskReadError(w http.ResponseWriter, err error) {
	httpStatus, code, message := http.StatusBadGateway, errcode.ErrInternal, "task read service error"
	switch status.Code(err) {
	case codes.InvalidArgument:
		httpStatus, code, message = http.StatusBadRequest, errcode.ErrBadRequest, "invalid task read request"
	case codes.Unauthenticated:
		httpStatus, code, message = http.StatusUnauthorized, errcode.ErrUnAuth, "invalid or expired login token"
	case codes.PermissionDenied:
		httpStatus, code, message = http.StatusForbidden, errcode.ErrForbidden, "team membership required"
	case codes.NotFound:
		httpStatus, code, message = http.StatusNotFound, errcode.ErrNotFound, "task or team not found"
	case codes.Aborted:
		httpStatus, message = http.StatusConflict, "task data changed; retry"
	case codes.Unavailable:
		httpStatus, message = http.StatusServiceUnavailable, "task read service unavailable"
	case codes.DeadlineExceeded:
		httpStatus, message = http.StatusGatewayTimeout, "task read service timeout"
	}
	httpx.WriteJson(w, httpStatus, taskReadResponse{Code: code, Msg: message})
}

func invalidTaskReadResponse() error {
	return status.Error(codes.Internal, "invalid task read response")
}

func validTaskReadItems(tasks []*taskpb.TaskItem) bool {
	if len(tasks) > 50 {
		return false
	}
	seen := make(map[int64]struct{}, len(tasks))
	for _, task := range tasks {
		if task == nil || task.TaskId <= 0 || task.TeamId <= 0 || task.CreatorId <= 0 || task.AssigneeId < 0 || task.Status < 0 || task.Status > 2 || task.DueAtUnixMs < 0 {
			return false
		}
		if !((task.SourceGroupId == 0 && task.SourceMessageId == 0) || (task.SourceGroupId > 0 && task.SourceMessageId > 0)) {
			return false
		}
		if _, exists := seen[task.TaskId]; exists {
			return false
		}
		seen[task.TaskId] = struct{}{}
	}
	return true
}

// enrichTaskReadItems reads only names required by this validated Task page.
func enrichTaskReadItems(ctx context.Context, client taskNamesClient, tasks []*taskpb.TaskItem) ([]enrichedTaskItem, error) {
	result := make([]enrichedTaskItem, 0, len(tasks))
	if len(tasks) == 0 {
		return result, nil
	}
	teamIDs := make([]int64, 0, len(tasks))
	usersByTeam := make(map[int64][]int64)
	seenByTeam := make(map[int64]map[int64]struct{})
	for _, task := range tasks {
		if _, exists := seenByTeam[task.TeamId]; !exists {
			teamIDs = append(teamIDs, task.TeamId)
			seenByTeam[task.TeamId] = make(map[int64]struct{})
		}
		for _, id := range []int64{task.CreatorId, task.AssigneeId} {
			if id == 0 {
				continue
			}
			if _, exists := seenByTeam[task.TeamId][id]; !exists {
				seenByTeam[task.TeamId][id] = struct{}{}
				usersByTeam[task.TeamId] = append(usersByTeam[task.TeamId], id)
			}
		}
	}
	teams, err := client.BatchGetMyTeamNames(ctx, &userpb.BatchGetMyTeamNamesRequest{TeamIds: teamIDs})
	if err != nil {
		return nil, err
	}
	if teams == nil {
		return nil, invalidTaskReadResponse()
	}
	teamNames := make(map[int64]string, len(teams.Teams))
	for _, team := range teams.Teams {
		if team == nil || team.TeamId <= 0 || team.Name == "" {
			return nil, invalidTaskReadResponse()
		}
		if _, requested := seenByTeam[team.TeamId]; !requested {
			return nil, invalidTaskReadResponse()
		}
		if _, duplicate := teamNames[team.TeamId]; duplicate {
			return nil, invalidTaskReadResponse()
		}
		teamNames[team.TeamId] = team.Name
	}
	memberNames := make(map[int64]map[int64]string, len(teamIDs))
	for _, teamID := range teamIDs {
		members, err := client.BatchGetTeamMemberDisplayNames(ctx, &userpb.BatchGetTeamMemberDisplayNamesRequest{TeamId: teamID, UserIds: usersByTeam[teamID]})
		if err != nil {
			return nil, err
		}
		if members == nil {
			return nil, invalidTaskReadResponse()
		}
		names := make(map[int64]string, len(members.Users))
		for _, member := range members.Users {
			if member == nil || member.UserId <= 0 || member.DisplayName == "" {
				return nil, invalidTaskReadResponse()
			}
			if _, requested := seenByTeam[teamID][member.UserId]; !requested {
				return nil, invalidTaskReadResponse()
			}
			if _, duplicate := names[member.UserId]; duplicate {
				return nil, invalidTaskReadResponse()
			}
			names[member.UserId] = member.DisplayName
		}
		memberNames[teamID] = names
	}
	for _, task := range tasks {
		result = append(result, enrichedTaskItem{
			TaskID: task.TaskId, TeamID: task.TeamId, TeamName: teamNames[task.TeamId], Title: task.Title, Description: task.Description,
			CreatorID: task.CreatorId, CreatorName: memberNames[task.TeamId][task.CreatorId],
			AssigneeID: task.AssigneeId, AssigneeName: memberNames[task.TeamId][task.AssigneeId], Status: task.Status,
			SourceGroupID: task.SourceGroupId, SourceMessageID: task.SourceMessageId, DueAtUnixMs: task.DueAtUnixMs,
		})
	}
	return result, nil
}
