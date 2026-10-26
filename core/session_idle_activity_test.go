package core

import (
	"context"
	"testing"
	"time"
)

type idleResumeAgent struct {
	cujAgent
	resumed []string
}

func (a *idleResumeAgent) StartSession(ctx context.Context, id string) (AgentSession, error) {
	a.mu.Lock()
	a.resumed = append(a.resumed, id)
	a.mu.Unlock()
	return a.cujAgent.StartSession(ctx, id)
}

func (a *idleResumeAgent) resumeIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.resumed...)
}

type idleActivitySession struct {
	*controllableAgentSession
	last time.Time
	busy bool
}

func (s *idleActivitySession) SessionActivity() (time.Time, bool) { return s.last, s.busy }

func TestAgentSessionIdleTimeout_PreservesRunningWorkerAndRecentOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		last time.Time
		busy bool
	}{
		{"running worker", time.Now().Add(-2 * time.Hour), true},
		{"recent background output", time.Now(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine()
			defer e.cancel()
			e.SetAgentSessionIdleTimeout(time.Hour)
			key := "test:idle-activity"
			s := &idleActivitySession{controllableAgentSession: newControllableSession("native-id"), last: tc.last, busy: tc.busy}
			state := &interactiveState{agentSession: s, eventsNeedResync: false, agentSessionIdleToken: 1}
			e.interactiveStates[key] = state
			e.cleanupInteractiveStateForIdleToken(key, state, 1, time.Hour)
			select {
			case <-s.closed:
				t.Fatal("closed a session with active work or recent output")
			default:
			}
		})
	}
}
