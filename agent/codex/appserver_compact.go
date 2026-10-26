package codex

import "fmt"

// Slash commands are plain model text in turn/start. Manual compaction uses
// the dedicated RPC and completes through the existing native turn events.
func (s *appServerSession) startCompaction() error {
	threadID := s.CurrentSessionID()
	if threadID == "" {
		return fmt.Errorf("codex compaction: thread id is empty")
	}
	s.stateMu.Lock()
	if s.currentTurn != "" || s.compacting {
		s.stateMu.Unlock()
		return fmt.Errorf("codex compaction: a turn is already running")
	}
	s.compacting = true
	s.stateMu.Unlock()
	var result struct{}
	if err := s.request("thread/compact/start", map[string]any{"threadId": threadID}, &result); err != nil {
		s.stateMu.Lock()
		s.compacting = false
		s.stateMu.Unlock()
		return fmt.Errorf("codex compaction: %w", err)
	}
	return nil
}
