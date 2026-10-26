package core

func (e *Engine) refreshAgentSessionIdleCloseForMessage(sessionKey string) {
	if e.agentSessionIdleTimeoutNanos.Load() <= 0 {
		return
	}
	key := e.interactiveKeyForSessionKey(sessionKey)
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if state != nil {
		e.scheduleAgentSessionIdleClose(key, state)
	}
}
