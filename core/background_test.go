package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

type backgroundTestAgent struct {
	stubAgent
	s *backgroundTestSession
}

func (a *backgroundTestAgent) StartSession(context.Context, string) (AgentSession, error) {
	return a.s, nil
}

type backgroundTestSession struct {
	*cujAgentSession
	background  chan Event
	chatStarted chan struct{}
	releaseChat chan struct{}
	decisions   chan struct {
		id     string
		result PermissionResult
	}
}

func (s *backgroundTestSession) BackgroundEvents() <-chan Event { return s.background }
func (s *backgroundTestSession) RespondPermission(id string, result PermissionResult) error {
	s.decisions <- struct {
		id     string
		result PermissionResult
	}{id, result}
	return nil
}
func (s *backgroundTestSession) Send(prompt, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	s.mu.Lock()
	s.sentPrompts = append(s.sentPrompts, prompt)
	s.mu.Unlock()
	response := "CHAT_OK"
	switch {
	case strings.Contains(prompt, "start work"):
		response = "WORK_DISPATCHED"
	case strings.Contains(prompt, "background completion"):
		response = "WORK_SUMMARY"
	case strings.Contains(prompt, "slow chat"):
		close(s.chatStarted)
		<-s.releaseChat
		response = "CHAT_WHILE_WORKING"
	}
	s.events <- Event{Type: EventResult, Content: response, Done: true}
	return nil
}

func newBackgroundTestEnv(t *testing.T) (*cujEnv, *backgroundTestSession) {
	env := newCUJEnv(t)
	s := &backgroundTestSession{cujAgentSession: newCUJAgentSession(), background: make(chan Event, 16), chatStarted: make(chan struct{}), releaseChat: make(chan struct{}), decisions: make(chan struct {
		id     string
		result PermissionResult
	}, 8)}
	env.engine.agent = &backgroundTestAgent{s: s}
	t.Cleanup(func() { env.engine.cancel(); env.engine.cleanupInteractiveState("test:boss") })
	return env, s
}

func TestBackgroundPermission_ChatContinuesAndOnlyUserCanApprove(t *testing.T) {
	env, s := newBackgroundTestEnv(t)
	env.userSends("boss", "start work")
	env.waitFor("dispatch", 2*time.Second, func() bool { return env.sentContains("WORK_DISPATCHED") })
	s.background <- Event{Type: EventPermissionRequest, RequestID: "worker-approval", ToolName: "Bash", ToolInput: "write report", ToolInputRaw: map[string]any{"command": "write report"}}
	env.waitFor("approval prompt", 2*time.Second, func() bool { return env.sentContains("write report") })
	select {
	case got := <-s.decisions:
		t.Fatalf("permission decided without user: %+v", got)
	default:
	}
	env.userSends("boss", "are you still here?")
	env.waitFor("chat while approval pending", 2*time.Second, func() bool { return env.sentContains("CHAT_OK") })
	env.userSends("boss", "allow")
	select {
	case got := <-s.decisions:
		if got.id != "worker-approval" || got.result.Behavior != "allow" {
			t.Fatalf("wrong decision: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("user approval did not reach worker")
	}
	s.background <- Event{Type: EventPermissionRequest, RequestID: "second-approval", ToolName: "Bash", ToolInput: "second command"}
	env.waitFor("second prompt", 2*time.Second, func() bool { return env.sentContains("second command") })
	env.userSends("boss", "deny")
	select {
	case got := <-s.decisions:
		if got.id != "second-approval" || got.result.Behavior != "deny" {
			t.Fatalf("wrong denial: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("user denial did not reach worker")
	}
}

func TestBackgroundFollowUp_DoesNotWakeReplacedSession(t *testing.T) {
	env, s := newBackgroundTestEnv(t)
	key := env.userSends("boss", "start work")
	env.waitFor("dispatch", 2*time.Second, func() bool { return env.sentContains("WORK_DISPATCHED") })
	env.waitFor("idle", 2*time.Second, func() bool { return !env.activeSession(key).Busy() })
	oldSession := env.activeSession(key)
	env.engine.interactiveMu.Lock()
	state := env.engine.interactiveStates[key]
	env.engine.interactiveMu.Unlock()
	env.engine.sessions.NewSession(key, "replacement")
	env.engine.runBackgroundFollowUp(state, s, oldSession, env.engine.sessions, key, key, Event{Content: "background completion"})
	if env.sentContains("WORK_SUMMARY") || len(s.getSentPrompts()) != 1 {
		t.Fatal("stale worker woke replacement chat")
	}
}
