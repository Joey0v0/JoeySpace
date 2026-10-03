package model

import (
	"strings"
	"testing"
)

func TestTaskCreatedCardPreservesConfirmedResult(t *testing.T) {
	for _, title := range []string{"修复缓存问题", "<img src=x onerror=alert(1)>", strings.Repeat("😀", 200)} {
		content, err := EncodeTaskCreatedCard(9223372036854775807, title)
		if err != nil {
			t.Fatal(err)
		}
		card, err := DecodeTaskCreatedCard(content)
		if err != nil || card.Version != 1 || card.TaskID != "9223372036854775807" || card.Title != title {
			t.Fatalf("lost task result: %+v err=%v", card, err)
		}
		repeated, err := EncodeTaskCreatedCard(9223372036854775807, title)
		if err != nil || repeated != content {
			t.Fatalf("same result must keep identical content: %q %q %v", content, repeated, err)
		}
	}
}

func TestTaskCreatedCardRejectsInvalidWireContent(t *testing.T) {
	for _, content := range []string{
		`null`, `[]`, `{}`, `{"version":1,"task_id":"9","title":"ok"} {}`,
		`{"version":2,"task_id":"9","title":"ok"}`,
		`{"version":"1","task_id":"9","title":"ok"}`,
		`{"version":1,"task_id":9007199254740993,"title":"ok"}`,
		`{"version":1,"task_id":"9223372036854775808","title":"ok"}`,
		`{"version":1,"task_id":"09","title":"ok"}`,
		`{"version":1,"task_id":"+9","title":"ok"}`,
		`{"version":1,"task_id":"0","title":"ok"}`,
		`{"version":1,"task_id":"9","title":" "}`,
		`{"version":1,"task_id":"9","title":" ok"}`,
		`{"version":1,"task_id":"9","Title":"ok"}`,
		`{"version":1,"task_id":"9","title":"ok","url":"https://example.invalid"}`,
		`{"version":1,"task_id":"9","title":"` + strings.Repeat("中", 201) + `"}`,
		strings.Repeat(" ", 4097), string([]byte{0xff}),
	} {
		if _, err := DecodeTaskCreatedCard(content); err == nil {
			t.Fatalf("invalid card accepted: %q", content)
		}
	}
}

func TestTaskCreatedCardRejectsInvalidResult(t *testing.T) {
	for _, tc := range []struct {
		id    int64
		title string
	}{
		{0, "title"}, {-1, "title"}, {9, ""}, {9, " title "}, {9, strings.Repeat("中", 201)},
	} {
		if _, err := EncodeTaskCreatedCard(tc.id, tc.title); err == nil {
			t.Fatalf("invalid result accepted: %+v", tc)
		}
	}
}
