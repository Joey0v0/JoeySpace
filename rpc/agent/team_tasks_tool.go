package agent

import (
	"context"
	"strconv"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type teamTaskToolResult struct {
	Tasks []teamTaskToolItem `json:"tasks"`
}

type teamTaskToolItem struct {
	TaskID          string `json:"task_id"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	Status          int32  `json:"status"`
	AssigneeID      string `json:"assignee_id"`
	DueAtUnixMs     int64  `json:"due_at_unix_ms"`
	SourceGroupID   string `json:"source_group_id"`
	SourceMessageID string `json:"source_message_id"`
}

// NewListTeamTasksTool binds the user's identity and team on the server side.
// The model receives no team_id or token argument and cannot change the scope.
func NewListTeamTasksTool(reader *ContextReader, token string, teamID int64) (tool.InvokableTool, error) {
	if reader == nil || reader.tasks == nil {
		return nil, status.Error(codes.Unavailable, "task context is not enabled")
	}
	if teamID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid team")
	}
	if token == "" {
		return nil, status.Error(codes.Unauthenticated, "login required")
	}
	return utils.InferTool[struct{}, teamTaskToolResult](
		"list_team_tasks",
		"读取当前用户有权访问的当前团队任务；只读，最多返回 20 条。无需参数。",
		func(ctx context.Context, _ struct{}) (teamTaskToolResult, error) {
			tasks, err := reader.TeamTasks(ctx, token, teamID)
			if err != nil {
				return teamTaskToolResult{}, err
			}
			result := teamTaskToolResult{Tasks: make([]teamTaskToolItem, 0, len(tasks))}
			for _, task := range tasks {
				if task == nil {
					continue
				}
				result.Tasks = append(result.Tasks, teamTaskToolItem{
					TaskID: strconv.FormatInt(task.GetTaskId(), 10), Title: task.GetTitle(),
					Description: task.GetDescription(), Status: task.GetStatus(),
					AssigneeID: strconv.FormatInt(task.GetAssigneeId(), 10), DueAtUnixMs: task.GetDueAtUnixMs(),
					SourceGroupID:   strconv.FormatInt(task.GetSourceGroupId(), 10),
					SourceMessageID: strconv.FormatInt(task.GetSourceMessageId(), 10),
				})
			}
			return result, nil
		},
	)
}
