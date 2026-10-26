package codex

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestAppServerSession_CompactUsesNativeRPC(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	s := &appServerSession{ctx: ctx, stdin: w, pending: make(map[int64]chan rpcResponseEnvelope), events: make(chan core.Event, 8)}
	s.alive.Store(true)
	s.threadID.Store("root")
	done := make(chan error, 1)
	go func() { done <- s.Send("/compact", "", nil, nil) }()
	var req struct {
		ID     int64
		Method string
		Params map[string]any
	}
	if err := json.NewDecoder(r).Decode(&req); err != nil {
		t.Fatal(err)
	}
	if req.Method != "thread/compact/start" || req.Params["threadId"] != "root" {
		t.Fatalf("compact sent as an ordinary model prompt: %+v", req)
	}
	s.handleResponse(rpcResponseEnvelope{ID: req.ID, Result: json.RawMessage(`{}`)})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("compact request did not return")
	}
	s.handleNotification("turn/started", json.RawMessage(`{"threadId":"root","turn":{"id":"compact-turn","status":"inProgress"}}`))
	s.handleNotification("item/completed", json.RawMessage(`{"threadId":"worker","item":{"type":"contextCompaction"}}`))
	if len(s.events) != 0 {
		t.Fatal("worker compaction completed root operation")
	}
	s.handleNotification("item/completed", json.RawMessage(`{"threadId":"root","item":{"type":"contextCompaction"}}`))
	s.handleNotification("thread/status/changed", json.RawMessage(`{"threadId":"root","status":{"type":"idle"}}`))
	if len(s.events) != 0 {
		t.Fatal("reported success before native turn status was known")
	}
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"root","turn":{"id":"compact-turn","status":"completed"}}`))
	if len(s.events) != 1 {
		t.Fatalf("got %d completion events, want one", len(s.events))
	}
	if event := <-s.events; event.Type != core.EventResult || !event.Done {
		t.Fatalf("invalid compact completion: %+v", event)
	}
}

func TestAppServerSession_CompactFailureIsNotReportedAsSuccess(t *testing.T) {
	s := &appServerSession{ctx: context.Background(), events: make(chan core.Event, 8), currentTurn: "compact-turn", compacting: true}
	s.threadID.Store("root")
	s.handleNotification("thread/status/changed", json.RawMessage(`{"threadId":"root","status":{"type":"idle"}}`))
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"root","turn":{"id":"compact-turn","status":"failed","error":{"message":"compaction failed"}}}`))
	if len(s.events) != 1 {
		t.Fatalf("got %d events", len(s.events))
	}
	if event := <-s.events; event.Type != core.EventError || event.Error == nil {
		t.Fatalf("expected failure, got %+v", event)
	}
	if s.compacting || s.currentTurn != "" {
		t.Fatal("failed compaction left session busy")
	}
}

func TestAppServerSession_AutoCompactionDoesNotFinishChat(t *testing.T) {
	s := &appServerSession{ctx: context.Background(), events: make(chan core.Event, 8), currentTurn: "chat-turn"}
	s.threadID.Store("root")
	s.handleNotification("item/completed", json.RawMessage(`{"threadId":"root","item":{"type":"contextCompaction"}}`))
	if s.currentTurn != "chat-turn" || len(s.events) != 0 {
		t.Fatal("native mid-turn compaction ended the chat")
	}
}

func TestCodexCompressCommand_OnlyAppServer(t *testing.T) {
	if got := (&Agent{backend: "app_server"}).CompressCommand(); got != "/compact" {
		t.Fatalf("app-server command=%q", got)
	}
	if got := (&Agent{backend: "exec"}).CompressCommand(); got != "" {
		t.Fatalf("exec must not submit a fake slash command: %q", got)
	}
}
