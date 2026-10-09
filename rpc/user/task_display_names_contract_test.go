package main

import (
	"os"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestF4TaskDisplayProtocolContract(t *testing.T) {
	service := pb.File_user_proto.Services().ByName("User")
	for _, name := range []protoreflect.Name{"BatchGetTeamMemberDisplayNames", "BatchGetMyTeamNames"} {
		if service.Methods().ByName(name) == nil {
			t.Errorf("User.%s is missing", name)
		}
	}
	for name, fields := range map[protoreflect.Name]map[protoreflect.Name]protoreflect.FieldNumber{
		"BatchGetTeamMemberDisplayNamesRequest":  {"team_id": 1, "user_ids": 2},
		"TeamMemberDisplayName":                  {"user_id": 1, "display_name": 2},
		"BatchGetTeamMemberDisplayNamesResponse": {"users": 1},
		"BatchGetMyTeamNamesRequest":             {"team_ids": 1},
		"MyTeamName":                             {"team_id": 1, "name": 2},
		"BatchGetMyTeamNamesResponse":            {"teams": 1},
	} {
		message := pb.File_user_proto.Messages().ByName(name)
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
	source, err := os.ReadFile("user.proto")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, contract := range []string{
		"repeated int64 user_ids = 2; // 去重后最多 100 个正 ID。",
		"repeated int64 team_ids = 1; // 去重后最多 100 个正 ID。",
	} {
		if !strings.Contains(text, contract) {
			t.Errorf("batch limit contract is missing: %s", contract)
		}
	}
}
