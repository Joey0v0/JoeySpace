package main

import (
	"testing"

	"github.com/yjydist/go-im/rpc/task/pb"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestF4TaskProtocolContract(t *testing.T) {
	service := pb.File_task_proto.Services().ByName("Task")
	for _, name := range []protoreflect.Name{"ListMyTasks", "GetTask", "ListMyTaskNotifications"} {
		if service.Methods().ByName(name) == nil {
			t.Errorf("Task.%s is missing", name)
		}
	}
	taskItem := pb.File_task_proto.Messages().ByName("TaskItem")
	if field := taskItem.Fields().ByName("team_id"); field == nil || field.Number() != 10 {
		t.Errorf("TaskItem.team_id must use field 10, got %v", field)
	}
	statusRequest := pb.File_task_proto.Messages().ByName("SetTaskStatusRequest")
	expected := statusRequest.Fields().ByName("expected_status")
	if expected == nil || expected.Number() != 4 || !expected.HasPresence() {
		t.Errorf("SetTaskStatusRequest.expected_status must be optional field 4, got %v", expected)
	}
	notification := pb.File_task_proto.Messages().ByName("TaskNotificationItem")
	for name, number := range map[protoreflect.Name]protoreflect.FieldNumber{
		"team_id": 8, "task_title": 9, "current_status": 10,
	} {
		if field := notification.Fields().ByName(name); field == nil || field.Number() != number {
			t.Errorf("TaskNotificationItem.%s must use field %d, got %v", name, number, field)
		}
	}
	for name, fields := range map[protoreflect.Name]map[protoreflect.Name]protoreflect.FieldNumber{
		"ListMyTasksRequest":              {"view": 1, "team_id": 2, "cursor": 3, "limit": 4},
		"ListMyTasksResponse":             {"tasks": 1, "next_cursor": 2},
		"GetTaskRequest":                  {"team_id": 1, "task_id": 2},
		"GetTaskResponse":                 {"task": 1, "can_update_status": 2},
		"ListMyTaskNotificationsRequest":  {"team_id": 1, "cursor": 2, "limit": 3},
		"ListMyTaskNotificationsResponse": {"notifications": 1, "next_cursor": 2, "unread_count": 3},
	} {
		message := pb.File_task_proto.Messages().ByName(name)
		if message == nil {
			t.Errorf("message %s is missing", name)
			continue
		}
		for fieldName, number := range fields {
			if field := message.Fields().ByName(fieldName); field == nil || field.Number() != number {
				t.Errorf("%s.%s must use field %d, got %v", name, fieldName, number, field)
			}
		}
	}
}
