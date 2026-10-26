package codex

import (
	"encoding/json"
	"time"
)

func (s *appServerSession) SessionActivity() (time.Time, bool) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.lastActivity, s.currentTurn != "" || s.compacting || len(s.activeSubagents) != 0
}

func (s *appServerSession) touchActivity() {
	s.stateMu.Lock()
	s.lastActivity = time.Now()
	s.stateMu.Unlock()
}

func (s *appServerSession) trackSessionActivity(method string, raw json.RawMessage) {
	switch method {
	case "turn/started", "turn/completed", "item/started", "item/completed",
		"item/agentMessage/delta", "command/exec/outputDelta",
		"item/fileChange/outputDelta", "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
	default:
		return // Usage updates and connection keepalives must not prevent sleep.
	}
	var scope struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &scope) != nil {
		return
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.lastActivity = time.Now()
	if !s.isChildThread(scope.ThreadID) {
		return
	}
	switch method {
	case "turn/started":
		if s.activeSubagents == nil {
			s.activeSubagents = make(map[string]string)
		}
		s.activeSubagents[scope.ThreadID] = scope.Turn.ID
	case "turn/completed":
		if turn := s.activeSubagents[scope.ThreadID]; turn == "" || turn == scope.Turn.ID {
			delete(s.activeSubagents, scope.ThreadID)
		}
	}
}
