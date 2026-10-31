package core

import "log/slog"

// steerBusySession bypasses the deferred turn queue only for agents that
// explicitly support mid-run input. Startup messages stay ordered until the
// first prompt has been written, then flushStartupSteering forwards them too.
func (e *Engine) steerBusySession(p Platform, msg *Message, key string, session *Session, sessions *SessionManager) bool {
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	if state == nil {
		e.interactiveMu.Unlock()
		return false
	}
	e.interactiveMu.Unlock()
	state.steerMu.Lock()
	defer state.steerMu.Unlock()
	state.mu.Lock()
	if state.stopped || !state.steerReady || len(state.pendingMessages) > 0 || state.agentSession == nil || !state.agentSession.Alive() {
		state.mu.Unlock()
		return false
	}
	steering, ok := state.agentSession.(SteeringAgentSession)
	state.mu.Unlock()
	if !ok {
		return false
	}
	prompt := e.buildSenderPrompt(msg.Content, msg.UserID, msg.UserName, msg.Platform, msg.SessionKey, msg.ChannelKey)
	handled, err := steering.TrySteer(prompt, msg.MessageID, msg.Images, msg.Files)
	if !handled {
		return false
	}
	state.mu.Lock()
	stopped := state.stopped
	if err == nil && msg.UserMessageTimeMs > state.currentTurnUserMessageTimeMs {
		state.currentTurnUserMessageTimeMs = msg.UserMessageTimeMs
	}
	state.mu.Unlock()
	if stopped {
		return true
	}
	if err != nil {
		// Do not retry through the turn queue: a partial RPC write is ambiguous.
		slog.Error("steering message failed; not queued for retry", "session", key, "msg_id", msg.MessageID, "error", err)
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPsSendFailed))
		return true
	}
	session.AddHistory("user", msg.Content)
	sessions.Save()
	runMessageAccepted(msg)
	slog.Info("message steered into active run", "session", key, "msg_id", msg.MessageID)
	return true
}

func (e *Engine) flushStartupSteering(state *interactiveState, session *Session, sessions *SessionManager) {
	state.steerMu.Lock()
	defer state.steerMu.Unlock()
	state.mu.Lock()
	if state.stopped {
		state.mu.Unlock()
		return
	}
	state.steerReady = true
	steering, ok := state.agentSession.(SteeringAgentSession)
	state.mu.Unlock()
	if !ok {
		return
	}
	for {
		state.mu.Lock()
		if state.stopped || len(state.pendingMessages) == 0 {
			state.mu.Unlock()
			break
		}
		q := state.pendingMessages[0]
		state.pendingMessages = state.pendingMessages[1:]
		state.mu.Unlock()
		prompt := e.buildSenderPrompt(q.content, q.userID, q.userName, q.msgPlatform, q.msgSessionKey, q.channelKey)
		handled, err := steering.TrySteer(prompt, q.messageID, q.images, q.files)
		state.mu.Lock()
		stopped := state.stopped
		if !handled && !stopped {
			state.pendingMessages = append([]queuedMessage{q}, state.pendingMessages...)
		}
		if handled && err == nil && q.userMessageTimeMs > state.currentTurnUserMessageTimeMs {
			state.currentTurnUserMessageTimeMs = q.userMessageTimeMs
		}
		state.mu.Unlock()
		if stopped || !handled {
			break
		}
		if err != nil {
			slog.Error("startup steering failed; not queued for retry", "msg_id", q.messageID, "error", err)
			e.reply(q.platform, q.replyCtx, e.i18n.T(MsgPsSendFailed))
			continue
		}
		session.AddHistory("user", q.content)
	}
	sessions.Save()
}
