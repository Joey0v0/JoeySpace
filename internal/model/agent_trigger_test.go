package model

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestAgentTaskCommandBoundaries(t *testing.T) {
	for _, text := range []string{"@AI 整理任务 修复缓存", " \t@AI\n整理任务\t修复缓存 \n"} {
		got, ok := ParseAgentTaskCommand(text)
		if !ok || got != "修复缓存" {
			t.Fatalf("%q: %q %v", text, got, ok)
		}
	}
	for _, text := range []string{"@AI 整理任务", "@AI 整理任务  ", "普通正文 @AI 整理任务 工作", "> @AI 整理任务 工作", "@AI整理任务 工作", "@AI 整理任务工作", "@ai 整理任务 工作", "@AI 整理任务 " + strings.Repeat("任", 2001), "@AI 整理任务 \xff"} {
		if _, ok := ParseAgentTaskCommand(text); ok {
			t.Fatalf("accepted %q", text)
		}
	}
	base := Message{FromID: 1, ToID: 2, ChatType: 2, ContentType: 1, Content: "@AI 整理任务 工作"}
	if _, ok := AgentTaskCommand(&base); !ok {
		t.Fatal("legacy user text must be eligible")
	}
	for _, mutate := range []func(*Message){func(m *Message) { m.SenderType = MessageSenderBot }, func(m *Message) { m.InitiatorID = 1 }, func(m *Message) { m.ChatType = 1 }, func(m *Message) { m.ContentType = 2 }, func(m *Message) { m.FromID = 0 }} {
		m := base
		mutate(&m)
		if _, ok := AgentTaskCommand(&m); ok {
			t.Fatal("ineligible message triggered")
		}
	}
}

func TestAgentTriggerNotificationHasOnlyStableReference(t *testing.T) {
	r := AgentTriggerOutbox{MessageID: 9007199254740993, Action: AgentTriggerAction, EventVersion: 1, MsgID: "client-id", ActorID: 1, TeamID: 2, GroupID: 3, Instruction: "明天修复", ReferenceTimeMS: 1791043200000}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(r.Event())
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"version":1,"action":"extract_tasks","message_id":"9007199254740993"}` {
		t.Fatalf("unexpected notification %s", b)
	}
	if r.RequestKey() != "agent-trigger:tasks:9007199254740993" {
		t.Fatal("unstable key")
	}
}

func TestAgentTriggerSchemaInitializationMatchesUpgrade(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		s := regexp.MustCompile(`(?m)--[^\r\n]*`).ReplaceAllString(string(b), "")
		return strings.Join(strings.Fields(s), " ")
	}
	p := regexp.MustCompile(`CREATE TABLE im_agent_trigger_outbox \(.*?\) ENGINE=InnoDB;`)
	a := p.FindString(read("../../deploy/mysql/init.sql"))
	b := p.FindString(read("../../deploy/mysql/migrations/022_im_agent_trigger_outbox.sql"))
	if a == "" || a != b {
		t.Fatal("initialization and 022 differ")
	}
}
