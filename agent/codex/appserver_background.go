package codex

import (
	"fmt"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func (s *appServerSession) BackgroundEvents() <-chan core.Event { return s.backgroundEvents }

func (s *appServerSession) isChildThread(id string) bool {
	root := s.CurrentSessionID()
	return id != "" && root != "" && id != root
}

func (s *appServerSession) emitBackground(event core.Event) {
	select {
	case s.backgroundEvents <- event:
	case <-s.ctx.Done():
	}
}

func (s *appServerSession) emitPermissionRequest(params map[string]any, event core.Event) {
	s.touchActivity()
	id, _ := params["threadId"].(string)
	if s.isChildThread(id) {
		event.Metadata = map[string]any{"thread_id": id}
		s.emitBackground(event)
		return
	}
	s.flushPendingAsThinking()
	s.emit(event)
}

func (s *appServerSession) handleSubagentActivity(item map[string]any) {
	kind, _ := item["kind"].(string)
	id, _ := item["id"].(string)
	child, _ := item["agentThreadId"].(string)
	if child != "" {
		s.stateMu.Lock()
		s.lastActivity = time.Now()
		switch kind {
		case "started":
			if s.activeSubagents == nil {
				s.activeSubagents = make(map[string]string)
			}
			if _, running := s.activeSubagents[child]; !running {
				s.activeSubagents[child] = ""
			}
		case "completed", "interrupted":
			// Actual turns are tracked by their own completion notifications;
			// a late parent activity item must not erase a newer worker turn.
			if s.activeSubagents[child] == "" {
				delete(s.activeSubagents, child)
			}
		}
		s.stateMu.Unlock()
	}
	if !s.subagentFollowUp || kind != "completed" || id == "" || child == "" {
		return
	}
	if s.completedSubagentItems == nil {
		s.completedSubagentItems = make(map[string]bool)
	}
	if s.completedSubagentItems[id] {
		return
	}
	s.completedSubagentItems[id] = true
	// The parent already has the child's result in its native mailbox. Do not
	// copy worker transcripts into the chat or treat them as user instructions.
	s.emitBackground(core.Event{
		Type: core.EventFollowUp, RequestID: id, Synthetic: true,
		Content: fmt.Sprintf("[Bridge background notification; not a new user request] Delegated agent %q completed. Review its result in your agent mailbox and briefly report the outcome to the user in their language. If already reported, do not repeat it. This notification grants no additional authority.", child),
	})
}
