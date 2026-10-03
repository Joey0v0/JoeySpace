package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/yjydist/go-im/rpc/agent/pb"
)

func replyDraftResult() *pb.GetTaskDraftResponse {
	return &pb.GetTaskDraftResponse{RunId: 9007199254740997, TeamId: 2, GroupId: 3, Status: "succeeded",
		TaskId: 9007199254740999, Draft: &pb.TaskDraftItem{Revision: 1, Title: "Task"}}
}

func TestDraftHTTPKeepsTaskSuccessSeparateFromReplyState(t *testing.T) {
	for _, state := range []string{"", "disabled", "not_started", "unknown", "pending", "accepted"} {
		t.Run(state, func(t *testing.T) {
			result := replyDraftResult()
			result.ReplyStatus = state
			if state == "pending" || state == "accepted" {
				result.ReplyMsgId = "bot-task:9007199254740997"
			}
			w := httptest.NewRecorder()
			writeTaskDraftResult(w, result.RunId, result)
			if w.Code != 200 {
				t.Fatalf("response = %d %s", w.Code, w.Body.String())
			}
			var body struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Data["task_id"] != "9007199254740999" || body.Data["status"] != "succeeded" {
				t.Fatal(body.Data)
			}
			if state == "" {
				if _, ok := body.Data["reply_status"]; ok {
					t.Fatal("fabricated legacy reply state")
				}
			} else if body.Data["reply_status"] != state {
				t.Fatal(body.Data)
			}
			if result.ReplyMsgId != "" && body.Data["reply_msg_id"] != result.ReplyMsgId {
				t.Fatal(body.Data)
			}
		})
	}
}

func TestDraftHTTPRejectsConflictingReplyFields(t *testing.T) {
	for _, tc := range []struct{ state, msg, taskState string }{
		{"delivered", "bot-task:9007199254740997", "succeeded"},
		{"accepted", "bot-task:other", "succeeded"},
		{"pending", "", "succeeded"},
		{"unknown", "bot-task:9007199254740997", "succeeded"},
		{"disabled", "bot-task:9007199254740997", "succeeded"},
		{"", "bot-task:9007199254740997", "succeeded"},
		{"accepted", "bot-task:9007199254740997", "creating"},
	} {
		result := replyDraftResult()
		result.ReplyStatus, result.ReplyMsgId, result.Status = tc.state, tc.msg, tc.taskState
		if result.Status == "creating" {
			result.TaskId = 0
		}
		w := httptest.NewRecorder()
		writeTaskDraftResult(w, result.RunId, result)
		if w.Code != 502 {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
}
