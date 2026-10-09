package main

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/task/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestListMyTasksCursorRejectsInvalidSnapshotAndTuple(t *testing.T) {
	good := `{"version":1,"view":0,"team_id":0,"snapshot_now_unix_ms":1791500000000,"snapshot_upper_task_id":9007199254740999,"last_bucket":1,"last_due_at_unix_ms":1791500000000,"last_task_id":9007199254740993,"last_status":0}`
	cases := []string{strings.Replace(good, `"version":1`, `"version":2`, 1), strings.Replace(good, `1791500000000`, `0`, 1), strings.Replace(good, `9007199254740999`, `0`, 1), strings.Replace(good, `"last_bucket":1`, `"last_bucket":4`, 1), strings.Replace(good, `"last_status":0`, `"last_status":2`, 1), strings.Replace(good, `"last_due_at_unix_ms":1791500000000`, `"last_due_at_unix_ms":0`, 1), strings.Replace(good, `"last_bucket":1`, `"last_bucket":0`, 1), strings.Replace(good, `"last_task_id":9007199254740993`, `"last_task_id":0`, 1), strings.Replace(good, `"last_task_id":9007199254740993`, `"last_task_id":9007199254741000`, 1), good + ` {}`, strings.Replace(good, `"view":0`, `"view":1`, 1), strings.Replace(good, `"team_id":0`, `"team_id":200`, 1), strings.Replace(good, `"version":1`, `"extra":true,"version":1`, 1)}
	s, _ := testPersonalServer(t)
	for _, data := range cases {
		if r, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{Cursor: base64.RawURLEncoding.EncodeToString([]byte(data))}); r != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid cursor %s: %v %v", data, r, err)
		}
	}
}

func TestPersonalCursorRejectsOmittedTupleFields(t *testing.T) {
	// Zero-valued fields still have to be present in the complete sort tuple.
	for _, data := range []string{
		`{"version":1,"view":0,"team_id":0,"snapshot_now_unix_ms":1791500000000,"snapshot_upper_task_id":9,"last_due_at_unix_ms":1791499999999,"last_task_id":8,"last_status":0}`,
		`{"version":1,"view":0,"team_id":0,"snapshot_now_unix_ms":1791500000000,"snapshot_upper_task_id":9,"last_bucket":0,"last_due_at_unix_ms":1791499999999,"last_task_id":8}`,
		`{"version":1,"view":0,"team_id":0,"snapshot_now_unix_ms":1791500000000,"snapshot_upper_task_id":9,"last_bucket":3,"last_task_id":8,"last_status":0}`,
	} {
		if _, err := decodePersonalTaskCursor(base64.RawURLEncoding.EncodeToString([]byte(data)), 0, 0); err == nil {
			t.Fatalf("incomplete cursor accepted: %s", data)
		}
	}
}

func TestPersonalCursorBucketBoundariesAndLargeIDs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bucket int
		due    int64
	}{
		{"overdue", 0, personalNow - 1}, {"due now", 1, personalNow}, {"due exactly week", 1, personalNow + 604800000}, {"due later", 2, personalNow + 604800001}, {"undated", 3, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := fmt.Sprintf(`{"version":1,"view":0,"team_id":9007199254740993,"snapshot_now_unix_ms":1791500000000,"snapshot_upper_task_id":9223372036854775807,"last_bucket":%d,"last_due_at_unix_ms":%d,"last_task_id":9223372036854775806,"last_status":1}`, tc.bucket, tc.due)
			c, err := decodePersonalTaskCursor(base64.RawURLEncoding.EncodeToString([]byte(data)), 0, 9007199254740993)
			if err != nil || c.LastTaskID != 9223372036854775806 || c.TeamID != 9007199254740993 {
				t.Fatalf("boundary cursor: %v %v", c, err)
			}
		})
	}
	for _, data := range []string{
		`{"version":1,"view":1,"team_id":0,"snapshot_now_unix_ms":1791500000000,"snapshot_upper_task_id":9,"last_bucket":1,"last_due_at_unix_ms":0,"last_task_id":8,"last_status":2}`,
		`{"version":1,"view":1,"team_id":0,"snapshot_now_unix_ms":1791500000000,"snapshot_upper_task_id":9,"last_bucket":0,"last_due_at_unix_ms":1,"last_task_id":8,"last_status":2}`,
		`{"version":1,"view":1,"team_id":0,"snapshot_now_unix_ms":9223372036854775807,"snapshot_upper_task_id":9,"last_bucket":0,"last_due_at_unix_ms":0,"last_task_id":8,"last_status":2}`,
	} {
		if _, err := decodePersonalTaskCursor(base64.RawURLEncoding.EncodeToString([]byte(data)), 1, 0); err == nil {
			t.Fatalf("bad completed tuple accepted: %s", data)
		}
	}
}
