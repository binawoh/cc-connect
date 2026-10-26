package codex

import (
	"context"
	"encoding/json"
	"github.com/chenhg5/cc-connect/core"
	"testing"
)

func TestAppServerActivity_WorkerSurvivesParentIdleAndResumes(t *testing.T) {
	s := &appServerSession{ctx: context.Background(), events: make(chan core.Event, 8)}
	s.threadID.Store("root")
	s.handleNotification("turn/started", json.RawMessage(`{"threadId":"worker","turn":{"id":"w1"}}`))
	last, busy := s.SessionActivity()
	if last.IsZero() || !busy {
		t.Fatal("worker was not tracked")
	}
	s.handleNotification("thread/status/changed", json.RawMessage(`{"threadId":"root","status":{"type":"idle"}}`))
	if _, busy := s.SessionActivity(); !busy {
		t.Fatal("parent idle hid running worker")
	}
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"worker","turn":{"id":"w1"}}`))
	if _, busy := s.SessionActivity(); busy {
		t.Fatal("finished worker remains active")
	}
	s.handleNotification("turn/started", json.RawMessage(`{"threadId":"worker","turn":{"id":"w2"}}`))
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"worker","turn":{"id":"w1"}}`))
	s.handleSubagentActivity(map[string]any{"type": "subAgentActivity", "kind": "completed", "id": "subagent-completed-w1", "agentThreadId": "worker"})
	if _, busy := s.SessionActivity(); !busy {
		t.Fatal("old completion erased resumed worker")
	}
	before, _ := s.SessionActivity()
	s.handleNotification("account/rateLimits/updated", json.RawMessage(`{}`))
	after, _ := s.SessionActivity()
	if !before.Equal(after) {
		t.Fatal("keepalive metadata reset inactivity")
	}
}
