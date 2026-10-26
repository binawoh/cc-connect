package core

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

func (e *Engine) startBackgroundReader(state *interactiveState, session *Session, sessions *SessionManager, key, ccKey string) {
	state.mu.Lock()
	source, ok := state.agentSession.(BackgroundEventSource)
	if !ok || state.backgroundReaderStarted {
		state.mu.Unlock()
		return
	}
	state.backgroundReaderStarted = true
	as := state.agentSession
	state.mu.Unlock()
	stop := state.stopSignal()
	go func() {
		for {
			select {
			case <-e.ctx.Done():
				return
			case <-stop:
				return
			case event, open := <-source.BackgroundEvents():
				if !open || !as.Alive() {
					return
				}
				switch event.Type {
				case EventFollowUp:
					e.runBackgroundFollowUp(state, as, session, sessions, key, ccKey, event)
				case EventPermissionRequest:
					e.processBackgroundPermission(state, as, event)
				}
			}
		}
	}()
}

// Acquire the same session gate as user messages. This prevents an automatic
// completion summary from interrupting a chat turn or consuming its events.
func (e *Engine) runBackgroundFollowUp(state *interactiveState, as AgentSession, session *Session, sessions *SessionManager, key, ccKey string, event Event) {
	if strings.TrimSpace(event.Content) == "" {
		return
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	stop := state.stopSignal()
	for {
		if e.ctx.Err() != nil || state.isStopped() || !as.Alive() {
			return
		}
		if session.TryLock() {
			break
		}
		select {
		case <-e.ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
		}
	}
	// A /new, /switch or cleanup while the worker was running must not wake
	// a replacement conversation with the old task's completion.
	e.interactiveMu.Lock()
	current := e.interactiveStates[key] == state
	e.interactiveMu.Unlock()
	sessions.mu.Lock()
	active := sessions.activeSession[ccKey] == session.ID
	sessions.mu.Unlock()
	state.mu.Lock()
	valid := current && active && !state.stopped && state.agentSession == as
	p, replyCtx, agent, workspace := state.platform, state.replyCtx, state.agent, state.workspaceDir
	state.mu.Unlock()
	if !valid {
		session.UnlockWithoutUpdate()
		return
	}
	slog.Info("background task waking parent", "session", key, "event_id", event.RequestID)
	e.processInteractiveMessageWith(p, &Message{
		SessionKey: ccKey, Platform: p.Name(), ReplyCtx: replyCtx,
		Content: event.Content, Synthetic: true,
	}, session, agent, sessions, key, workspace, ccKey)
}

// Both readers use one permission slot. Waiting for an existing request's
// resolution avoids replacing an approval card with an unrelated request.
func (e *Engine) installPendingPermission(state *interactiveState, pending *pendingPermission) bool {
	stop := state.stopSignal()
	for {
		state.mu.Lock()
		if state.stopped || e.ctx.Err() != nil {
			state.mu.Unlock()
			return false
		}
		previous := state.pending
		if previous == nil {
			state.pending = pending
			state.mu.Unlock()
			return true
		}
		state.mu.Unlock()
		select {
		case <-previous.Resolved:
		case <-stop:
			return false
		case <-e.ctx.Done():
			return false
		}
	}
}

func (e *Engine) processBackgroundPermission(state *interactiveState, as AgentSession, event Event) {
	state.mu.Lock()
	autoApprove := state.approveAll && len(event.Questions) == 0
	p, replyCtx := state.platform, state.replyCtx
	state.mu.Unlock()
	if autoApprove {
		if err := as.RespondPermission(event.RequestID, PermissionResult{Behavior: "allow", UpdatedInput: event.ToolInputRaw}); err != nil {
			slog.Error("background permission response failed", "error", err)
		}
		return
	}
	pending := &pendingPermission{
		Background: true, RequestID: event.RequestID, ToolName: event.ToolName,
		ToolInput: event.ToolInputRaw, InputPreview: event.ToolInput,
		Questions: event.Questions, Resolved: make(chan struct{}),
	}
	if !e.installPendingPermission(state, pending) {
		return
	}
	e.cancelAgentSessionIdleClose(state)
	if len(event.Questions) > 0 {
		e.sendAskQuestionPrompt(p, replyCtx, event.Questions, 0)
	} else {
		limit := e.display.ToolMaxLen * 8 / 5
		input := truncateIf(event.ToolInput, limit)
		prompt := fmt.Sprintf(e.i18n.T(MsgPermissionPrompt), event.ToolName, input)
		e.sendPermissionPrompt(p, replyCtx, prompt, event.ToolName, input)
	}
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()
	select {
	case <-pending.Resolved:
		return
	case <-state.stopSignal():
	case <-e.ctx.Done():
	case <-timer.C:
	}
	state.mu.Lock()
	if state.pending == pending {
		state.pending = nil
	}
	state.mu.Unlock()
	pending.resolve()
	if err := as.RespondPermission(event.RequestID, PermissionResult{Behavior: "deny", Message: "permission request expired or session stopped"}); err != nil {
		slog.Debug("background permission expired", "error", err)
	}
}
