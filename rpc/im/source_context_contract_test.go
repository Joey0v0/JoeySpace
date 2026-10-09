package main

import (
	"os"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestF4SourceContextProtocolContract(t *testing.T) {
	service := pb.File_rpc_im_im_proto.Services().ByName("IM")
	if service.Methods().ByName("GetTeamGroupMessageContext") == nil {
		t.Fatal("IM.GetTeamGroupMessageContext is missing")
	}
	for name, fields := range map[protoreflect.Name]map[protoreflect.Name]protoreflect.FieldNumber{
		"GetTeamGroupMessageContextRequest":  {"team_id": 1, "group_id": 2, "message_id": 3},
		"GetTeamGroupMessageContextResponse": {"messages": 1, "target_message_id": 2},
	} {
		message := pb.File_rpc_im_im_proto.Messages().ByName(name)
		if message == nil {
			t.Fatalf("message %s is missing", name)
		}
		if message.Fields().Len() != len(fields) {
			t.Errorf("message %s must have exactly %d fields, got %d", name, len(fields), message.Fields().Len())
		}
		for fieldName, number := range fields {
			if field := message.Fields().ByName(fieldName); field == nil || field.Number() != number {
				t.Errorf("%s.%s must use field %d, got %v", name, fieldName, number, field)
			}
		}
	}
	source, err := os.ReadFile("im.proto")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "目标前 20 条、后 20 条") || !strings.Contains(text, "最多 41 条") || !strings.Contains(text, "按消息 ID 升序") {
		t.Fatal("source context fixed 20+target+20 ascending contract is missing")
	}
}
