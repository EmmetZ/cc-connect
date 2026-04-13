package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type stubObservedSession struct {
	path       string
	events     chan Event
	closeOnce  sync.Once
	closeCalls atomic.Int32
}

func (s *stubObservedSession) Events() <-chan Event { return s.events }
func (s *stubObservedSession) SourcePath() string   { return s.path }
func (s *stubObservedSession) Close() error {
	s.closeOnce.Do(func() {
		s.closeCalls.Add(1)
		close(s.events)
	})
	return nil
}

func (s *stubObservedSession) CloseCalls() int {
	return int(s.closeCalls.Load())
}

type stubObserveAgent struct {
	stubAgent
	name      string
	observeFn func(ctx context.Context, sessionID string) (ObservedSession, error)
}

func (a *stubObserveAgent) Name() string {
	if strings.TrimSpace(a.name) != "" {
		return a.name
	}
	return "codex"
}

func (a *stubObserveAgent) ObserveSession(ctx context.Context, sessionID string) (ObservedSession, error) {
	if a.observeFn == nil {
		return nil, errors.New("observeFn not set")
	}
	return a.observeFn(ctx, sessionID)
}

func TestCmdObserveStartsCurrentSession(t *testing.T) {
	obs := &stubObservedSession{
		path:   "/tmp/rollout.jsonl",
		events: make(chan Event, 1),
	}
	agent := &stubObserveAgent{
		observeFn: func(_ context.Context, sessionID string) (ObservedSession, error) {
			if sessionID != "sess-123" {
				t.Fatalf("sessionID = %q, want sess-123", sessionID)
			}
			return obs, nil
		},
	}
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })

	e.sessions.GetOrCreateActive("telegram:1:1").SetAgentSessionID("sess-123", "codex")

	e.cmdObserve(p, &Message{SessionKey: "telegram:1:1", ReplyCtx: "rctx"}, nil)

	state := e.getObserveState()
	if state == nil {
		t.Fatal("expected observe state to be created")
	}
	if state.sessionID != "sess-123" {
		t.Fatalf("state.sessionID = %q, want sess-123", state.sessionID)
	}
	if state.sourcePath != "/tmp/rollout.jsonl" {
		t.Fatalf("state.sourcePath = %q, want /tmp/rollout.jsonl", state.sourcePath)
	}

	sent := strings.Join(p.getSent(), "\n")
	if !strings.Contains(sent, "sess-123") {
		t.Fatalf("start reply missing session id: %q", sent)
	}
}

func TestCmdObserveAlreadyCompleteDoesNotStart(t *testing.T) {
	agent := &stubObserveAgent{
		observeFn: func(_ context.Context, _ string) (ObservedSession, error) {
			return nil, ErrObservedSessionComplete
		},
	}
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	e.sessions.GetOrCreateActive("telegram:1:1").SetAgentSessionID("sess-123", "codex")

	e.cmdObserve(p, &Message{SessionKey: "telegram:1:1", ReplyCtx: "rctx"}, nil)

	if state := e.getObserveState(); state != nil {
		t.Fatal("observe state should stay nil when session is already complete")
	}
	if sent := strings.Join(p.getSent(), "\n"); !strings.Contains(sent, "sess-123") {
		t.Fatalf("reply missing session id: %q", sent)
	}
}

func TestCmdObserveStatusReportsActiveSession(t *testing.T) {
	obs := &stubObservedSession{
		path:   "/tmp/rollout.jsonl",
		events: make(chan Event, 1),
	}
	agent := &stubObserveAgent{
		observeFn: func(_ context.Context, _ string) (ObservedSession, error) {
			return obs, nil
		},
	}
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })

	e.sessions.GetOrCreateActive("telegram:1:1").SetAgentSessionID("sess-123", "codex")

	e.cmdObserve(p, &Message{SessionKey: "telegram:1:1", ReplyCtx: "rctx"}, nil)
	p.clearSent()

	e.cmdObserve(p, &Message{SessionKey: "telegram:1:1", ReplyCtx: "rctx"}, []string{"status"})

	sent := strings.Join(p.getSent(), "\n")
	if !strings.Contains(sent, "sess-123") || !strings.Contains(sent, "/tmp/rollout.jsonl") {
		t.Fatalf("status reply missing observe details: %q", sent)
	}
}

func TestCmdObserveStopStopsObserver(t *testing.T) {
	obs := &stubObservedSession{
		path:   "/tmp/rollout.jsonl",
		events: make(chan Event, 1),
	}
	agent := &stubObserveAgent{
		observeFn: func(_ context.Context, _ string) (ObservedSession, error) {
			return obs, nil
		},
	}
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	e.sessions.GetOrCreateActive("telegram:1:1").SetAgentSessionID("sess-123", "codex")

	e.cmdObserve(p, &Message{SessionKey: "telegram:1:1", ReplyCtx: "rctx"}, nil)
	p.clearSent()

	e.cmdObserve(p, &Message{SessionKey: "telegram:1:1", ReplyCtx: "rctx"}, []string{"stop"})

	if state := e.getObserveState(); state != nil {
		t.Fatal("observe state should be cleared after stop")
	}
	if obs.CloseCalls() != 1 {
		t.Fatalf("CloseCalls() = %d, want 1", obs.CloseCalls())
	}
}

func TestObservedSessionStopsWhenCurrentSessionChanges(t *testing.T) {
	prev := observeGuardInterval
	observeGuardInterval = 10 * time.Millisecond
	t.Cleanup(func() { observeGuardInterval = prev })

	obs := &stubObservedSession{
		path:   "/tmp/rollout.jsonl",
		events: make(chan Event, 4),
	}
	agent := &stubObserveAgent{
		observeFn: func(_ context.Context, _ string) (ObservedSession, error) {
			return obs, nil
		},
	}
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })

	s := e.sessions.GetOrCreateActive("telegram:1:1")
	s.SetAgentSessionID("sess-123", "codex")

	e.cmdObserve(p, &Message{SessionKey: "telegram:1:1", ReplyCtx: "rctx"}, nil)

	s.SetAgentSessionID("sess-999", "codex")

	waitForObserveStateCleared(t, e, 2*time.Second)
}

func TestObservedSessionDoesNotAppendHistory(t *testing.T) {
	obs := &stubObservedSession{
		path:   "/tmp/rollout.jsonl",
		events: make(chan Event, 4),
	}
	obs.events <- Event{Type: EventText, Content: "desktop output", SessionID: "sess-123"}
	obs.events <- Event{Type: EventResult, Content: "desktop output", SessionID: "sess-123", Done: true}

	agent := &stubObserveAgent{
		observeFn: func(_ context.Context, _ string) (ObservedSession, error) {
			return obs, nil
		},
	}
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })

	s := e.sessions.GetOrCreateActive("telegram:1:1")
	s.SetAgentSessionID("sess-123", "codex")

	e.cmdObserve(p, &Message{SessionKey: "telegram:1:1", ReplyCtx: "rctx"}, nil)

	waitForObserveStateCleared(t, e, 2*time.Second)

	if got := len(s.GetHistory(0)); got != 0 {
		t.Fatalf("history len = %d, want 0", got)
	}
}

func waitForObserveStateCleared(t *testing.T, e *Engine, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if e.getObserveState() == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("observe state was not cleared before timeout")
}
