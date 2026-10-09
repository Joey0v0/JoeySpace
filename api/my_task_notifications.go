package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"

	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/rest/httpx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type myTaskNotificationLister interface {
	ListMyTaskNotifications(context.Context, *taskpb.ListMyTaskNotificationsRequest, ...grpc.CallOption) (*taskpb.ListMyTaskNotificationsResponse, error)
}
type myTaskNotificationData struct {
	Notifications []myTaskNotificationItem `json:"notifications"`
	NextCursor    string                   `json:"next_cursor"`
	UnreadCount   int64                    `json:"unread_count,string"`
}
type myTaskNotificationItem struct {
	NotificationID  int64  `json:"notification_id,string"`
	TeamID          int64  `json:"team_id,string"`
	TeamName        string `json:"team_name"`
	TaskID          int64  `json:"task_id,string"`
	TaskTitle       string `json:"task_title"`
	ActorID         int64  `json:"actor_id,string"`
	ActorName       string `json:"actor_name"`
	FromStatus      int32  `json:"from_status"`
	ToStatus        int32  `json:"to_status"`
	CurrentStatus   int32  `json:"current_status"`
	CreatedAtUnixMs int64  `json:"created_at_unix_ms,string"`
	ReadAtUnixMs    int64  `json:"read_at_unix_ms,string"`
}

const maxBrowserTaskTime int64 = 253402300799999

func parseMyTaskNotificationsQuery(raw string) (*taskpb.ListMyTaskNotificationsRequest, error) {
	bad := status.Error(codes.InvalidArgument, "invalid notification filters")
	query, err := url.ParseQuery(raw)
	if err != nil {
		return nil, bad
	}
	for key, values := range query {
		if len(values) != 1 || (key != "team_id" && key != "cursor" && key != "limit") {
			return nil, bad
		}
	}
	req := &taskpb.ListMyTaskNotificationsRequest{Limit: 20}
	if values, ok := query["team_id"]; ok {
		req.TeamId, err = taskReadDecimal(values[0])
		if err != nil || values[0] != fmt.Sprint(req.TeamId) {
			return nil, bad
		}
	}
	if values, ok := query["cursor"]; ok {
		value := values[0]
		if value == "" || len(value) > 2048 {
			return nil, bad
		}
		for _, c := range value {
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				return nil, bad
			}
		}
		req.Cursor = value
	}
	if values, ok := query["limit"]; ok {
		value, parseErr := taskReadDecimal(values[0])
		if parseErr != nil || values[0] != fmt.Sprint(value) || value < 1 || value > 50 {
			return nil, bad
		}
		req.Limit = int32(value)
	}
	return req, nil
}

func listMyTaskNotificationsHandler(client myTaskNotificationLister, names taskNamesClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, ok := taskReadAuth(w, r)
		if !ok {
			return
		}
		req, err := parseMyTaskNotificationsQuery(r.URL.RawQuery)
		if err != nil {
			writeTaskReadError(w, err)
			return
		}
		result, err := client.ListMyTaskNotifications(ctx, req)
		if err != nil {
			writeTaskReadError(w, err)
			return
		}
		if !validMyTaskNotificationPage(result, int(req.Limit)) {
			writeTaskReadError(w, invalidTaskReadResponse())
			return
		}
		items, err := enrichMyTaskNotifications(ctx, names, result.Notifications)
		if err != nil {
			writeTaskReadError(w, err)
			return
		}
		httpx.WriteJson(w, http.StatusOK, taskReadResponse{Code: 0, Msg: "success", Data: myTaskNotificationData{Notifications: items, NextCursor: result.NextCursor, UnreadCount: result.UnreadCount}})
	}
}

func validMyTaskNotificationPage(result *taskpb.ListMyTaskNotificationsResponse, limit int) bool {
	if result == nil || result.UnreadCount < 0 || len(result.Notifications) > limit || len(result.NextCursor) > 2048 || (result.NextCursor != "" && len(result.Notifications) != limit) {
		return false
	}
	for _, c := range result.NextCursor {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	seen := map[int64]struct{}{}
	for _, item := range result.Notifications {
		if item == nil || item.NotificationId <= 0 || item.TeamId <= 0 || item.TaskId <= 0 || item.ActorId <= 0 || item.TaskTitle == "" || item.FromStatus < 0 || item.FromStatus > 2 || item.ToStatus < 0 || item.ToStatus > 2 || item.FromStatus == item.ToStatus || item.CurrentStatus < 0 || item.CurrentStatus > 2 || item.CreatedAtUnixMs <= 0 || item.CreatedAtUnixMs > maxBrowserTaskTime || item.ReadAtUnixMs < 0 || item.ReadAtUnixMs > maxBrowserTaskTime || (item.ReadAtUnixMs > 0 && item.ReadAtUnixMs < item.CreatedAtUnixMs) {
			return false
		}
		if _, exists := seen[item.NotificationId]; exists {
			return false
		}
		seen[item.NotificationId] = struct{}{}
	}
	return true
}

func enrichMyTaskNotifications(ctx context.Context, names taskNamesClient, rows []*taskpb.TaskNotificationItem) ([]myTaskNotificationItem, error) {
	result := make([]myTaskNotificationItem, 0, len(rows))
	if len(rows) == 0 {
		return result, nil
	}
	teamsSet := map[int64]struct{}{}
	actors := map[int64]map[int64]struct{}{}
	for _, row := range rows {
		teamsSet[row.TeamId] = struct{}{}
		if actors[row.TeamId] == nil {
			actors[row.TeamId] = map[int64]struct{}{}
		}
		actors[row.TeamId][row.ActorId] = struct{}{}
	}
	teams := make([]int64, 0, len(teamsSet))
	for id := range teamsSet {
		teams = append(teams, id)
	}
	sort.Slice(teams, func(i, j int) bool { return teams[i] < teams[j] })
	teamResponse, err := names.BatchGetMyTeamNames(ctx, &userpb.BatchGetMyTeamNamesRequest{TeamIds: teams})
	if err != nil || teamResponse == nil {
		if err != nil {
			return nil, err
		}
		return nil, invalidTaskReadResponse()
	}
	teamNames := map[int64]string{}
	seenTeams := map[int64]struct{}{}
	for _, team := range teamResponse.Teams {
		_, requested := teamsSet[team.GetTeamId()]
		if team == nil || team.TeamId <= 0 || !requested {
			return nil, invalidTaskReadResponse()
		}
		if _, duplicate := seenTeams[team.TeamId]; duplicate {
			return nil, invalidTaskReadResponse()
		}
		seenTeams[team.TeamId] = struct{}{}
		teamNames[team.TeamId] = team.Name
	}
	actorNames := map[int64]map[int64]string{}
	for _, teamID := range teams {
		ids := make([]int64, 0, len(actors[teamID]))
		for id := range actors[teamID] {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		response, callErr := names.BatchGetTeamMemberDisplayNames(ctx, &userpb.BatchGetTeamMemberDisplayNamesRequest{TeamId: teamID, UserIds: ids})
		if callErr != nil {
			return nil, callErr
		}
		if response == nil {
			return nil, invalidTaskReadResponse()
		}
		actorNames[teamID] = map[int64]string{}
		for _, user := range response.Users {
			_, requested := actors[teamID][user.GetUserId()]
			if user == nil || user.UserId <= 0 || !requested {
				return nil, invalidTaskReadResponse()
			}
			if _, duplicate := actorNames[teamID][user.UserId]; duplicate {
				return nil, invalidTaskReadResponse()
			}
			actorNames[teamID][user.UserId] = user.DisplayName
		}
	}
	for _, row := range rows {
		result = append(result, myTaskNotificationItem{NotificationID: row.NotificationId, TeamID: row.TeamId, TeamName: teamNames[row.TeamId], TaskID: row.TaskId, TaskTitle: row.TaskTitle, ActorID: row.ActorId, ActorName: actorNames[row.TeamId][row.ActorId], FromStatus: row.FromStatus, ToStatus: row.ToStatus, CurrentStatus: row.CurrentStatus, CreatedAtUnixMs: row.CreatedAtUnixMs, ReadAtUnixMs: row.ReadAtUnixMs})
	}
	return result, nil
}
