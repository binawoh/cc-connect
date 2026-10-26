package codex

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Replays the interleaved root/worker notifications observed on Codex 0.162.1.
func TestAppServerSession_ChildNotificationsDoNotFinishParentTurn(t *testing.T) {
	s := &appServerSession{ctx: context.Background(), events: make(chan core.Event, 16)}
	s.threadID.Store("root")
	notify := func(method, params string) { s.handleNotification(method, json.RawMessage(params)) }
	notify("turn/started", `{"threadId":"root","turn":{"id":"root-turn"}}`)
	notify("item/completed", `{"threadId":"root","item":{"type":"agentMessage","text":"root reply"}}`)
	notify("turn/started", `{"threadId":"worker","turn":{"id":"worker-turn"}}`)
	notify("item/completed", `{"threadId":"worker","item":{"type":"agentMessage","text":"private worker log"}}`)
	notify("turn/completed", `{"threadId":"worker","turn":{"id":"worker-turn"}}`)
	notify("thread/status/changed", `{"threadId":"worker","status":{"type":"idle"}}`)
	if s.currentTurn != "root-turn" || len(s.events) != 0 {
		t.Fatalf("child changed parent turn: turn=%q events=%d", s.currentTurn, len(s.events))
	}
	notify("turn/completed", `{"threadId":"root","turn":{"id":"root-turn"}}`)
	text := <-s.events
	if text.Type != core.EventText || text.Content != "root reply" {
		t.Fatalf("parent text contaminated: %+v", text)
	}
	if result := <-s.events; result.Type != core.EventResult || !result.Done {
		t.Fatalf("missing parent completion: %+v", result)
	}
}

func TestAppServerSession_SubagentCompletionWakesParentOnce(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s := &appServerSession{ctx: context.Background(), events: make(chan core.Event, 8),
			backgroundEvents: make(chan core.Event, 8), subagentFollowUp: enabled}
		s.threadID.Store("root")
		raw := json.RawMessage(`{"threadId":"root","item":{"type":"subAgentActivity","kind":"completed","id":"completion-1","agentThreadId":"worker"}}`)
		s.handleNotification("item/completed", raw)
		s.handleNotification("item/completed", raw)
		want := 0
		if enabled {
			want = 1
		}
		if len(s.backgroundEvents) != want || len(s.events) != 0 {
			t.Fatalf("enabled=%v background=%d foreground=%d", enabled, len(s.backgroundEvents), len(s.events))
		}
		if enabled {
			event := <-s.backgroundEvents
			if event.Type != core.EventFollowUp || !event.Synthetic || event.RequestID != "completion-1" {
				t.Fatalf("invalid completion: %+v", event)
			}
		}
	}
}

func TestAppServerSession_WorkerPermissionDoesNotFlushParentReply(t *testing.T) {
	s := &appServerSession{ctx: context.Background(), events: make(chan core.Event, 8), backgroundEvents: make(chan core.Event, 8), pendingMsgs: []string{"parent reply"}}
	s.threadID.Store("root")
	s.emitPermissionRequest(map[string]any{"threadId": "worker"}, core.Event{Type: core.EventPermissionRequest, RequestID: "approval-7"})
	if len(s.events) != 0 || len(s.pendingMsgs) != 1 || len(s.backgroundEvents) != 1 {
		t.Fatal("worker permission disturbed foreground stream")
	}
	if got := <-s.backgroundEvents; got.RequestID != "approval-7" || got.Metadata["thread_id"] != "worker" {
		t.Fatalf("lost permission routing: %+v", got)
	}
}
