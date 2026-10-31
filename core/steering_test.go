package core

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type steeringTestSession struct {
	closedState atomic.Bool
	*controllableAgentSession
	mu           sync.Mutex
	prompts      []string
	steers       []string
	err          error
	steerStarted chan struct{}
	steerRelease chan struct{}
}

func (s *steeringTestSession) Alive() bool { return !s.closedState.Load() }
func (s *steeringTestSession) Close() error {
	if s.closedState.CompareAndSwap(false, true) {
		close(s.events)
		close(s.closed)
	}
	return nil
}

func (s *steeringTestSession) Send(prompt, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts = append(s.prompts, prompt)
	return nil // Deliberately keep the run open until /stop.
}

func (s *steeringTestSession) TrySteer(prompt, _ string, _ []ImageAttachment, _ []FileAttachment) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steers = append(s.steers, prompt)
	if s.steerStarted != nil {
		close(s.steerStarted)
		<-s.steerRelease
	}
	return true, s.err
}

func TestCUJ_C5_SteerAndStopWithoutDeferredTurns(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	s := &steeringTestSession{controllableAgentSession: newControllableSession("steering")}
	e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	send := func(content string) {
		e.ReceiveMessage(p, &Message{SessionKey: "test:bob", Platform: "test", UserID: "bob", UserName: "bob", MessageID: content, Content: content, ReplyCtx: "ctx"})
	}
	send("long task")
	deadline := time.Now().Add(2 * time.Second)
	for {
		e.interactiveMu.Lock()
		state := e.interactiveStates["test:bob"]
		e.interactiveMu.Unlock()
		ready := false
		if state != nil {
			state.mu.Lock()
			ready = state.steerReady
			state.mu.Unlock()
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initial prompt never became ready")
		}
		time.Sleep(time.Millisecond)
	}
	send("change direction")
	send("use the latest instruction")
	s.mu.Lock()
	if len(s.prompts) != 1 || len(s.steers) != 2 {
		t.Fatalf("prompts=%v steers=%v", s.prompts, s.steers)
	}
	s.mu.Unlock()
	for _, text := range p.getSent() {
		if strings.Contains(text, e.i18n.T(MsgMessageQueued)) {
			t.Fatalf("unexpected deferred queue reply: %q", text)
		}
	}
	// Cancellation in ordinary language is interpreted by the agent, not
	// intercepted by engine phrase matching or command aliases.
	send("别发了")
	s.mu.Lock()
	if len(s.steers) != 3 || s.steers[2] != "别发了" {
		t.Fatalf("cancellation text was not steered: %v", s.steers)
	}
	s.mu.Unlock()
	if !s.Alive() {
		t.Fatal("natural-language cancellation was intercepted as a command")
	}
	send("/stop")
	select {
	case <-s.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not close the active agent")
	}
	found := false
	for _, text := range p.getSent() {
		if strings.Contains(text, e.i18n.T(MsgExecutionStopped)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing stop acknowledgement: %v", p.getSent())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.prompts) != 1 || len(s.steers) != 3 {
		t.Fatal("stop was sent to agent instead of interrupting it")
	}
}

func TestSteerFailureIsNotDeferredForRetry(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	s := &steeringTestSession{controllableAgentSession: newControllableSession("err"), err: fmt.Errorf("broken pipe")}
	e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, "", LangEnglish)
	state := &interactiveState{agentSession: s, steerReady: true}
	e.interactiveStates["test:bob"] = state
	msg := &Message{SessionKey: "test:bob", Content: "cancel"}
	if !e.steerBusySession(p, msg, msg.SessionKey, e.sessions.GetOrCreateActive(msg.SessionKey), e.sessions) {
		t.Fatal("failed steering must still be handled")
	}
	if len(state.pendingMessages) != 0 {
		t.Fatal("failed steering was queued for retry")
	}
}

func TestStartupQueueIsSteeredAfterInitialPrompt(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	s := &steeringTestSession{controllableAgentSession: newControllableSession("startup")}
	e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, "", LangEnglish)
	state := &interactiveState{agentSession: s, pendingMessages: []queuedMessage{{content: "first", platform: p}, {content: "second", platform: p}}}
	e.flushStartupSteering(state, e.sessions.GetOrCreateActive("test:bob"), e.sessions)
	if len(state.pendingMessages) != 0 || len(s.steers) != 2 || s.steers[0] != "first" || s.steers[1] != "second" {
		t.Fatal("startup messages were not forwarded as ordered steering")
	}
}

func TestStopDoesNotWaitForSteeringWrite(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	s := &steeringTestSession{controllableAgentSession: newControllableSession("blocked"), steerStarted: make(chan struct{}), steerRelease: make(chan struct{})}
	e := NewEngine("test", &controllableAgent{nextSession: s}, []Platform{p}, "", LangEnglish)
	state := &interactiveState{agentSession: s, steerReady: true}
	e.interactiveStates["test:bob"] = state
	done := make(chan struct{})
	go func() {
		e.steerBusySession(p, &Message{SessionKey: "test:bob", Content: "change"}, "test:bob", e.sessions.GetOrCreateActive("test:bob"), e.sessions)
		close(done)
	}()
	select {
	case <-s.steerStarted:
	case <-time.After(time.Second):
		t.Fatal("steer never started")
	}
	stopped := make(chan struct{})
	go func() { e.stopInteractiveSessionSilently("test:bob"); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		close(s.steerRelease)
		t.Fatal("stop blocked behind steering write")
	}
	close(s.steerRelease)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("steer did not finish")
	}
	if len(p.getSent()) != 0 {
		t.Fatalf("stopped steer sent replies: %v", p.getSent())
	}
}
