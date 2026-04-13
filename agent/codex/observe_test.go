package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestFindRolloutFile(t *testing.T) {
	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "04", "13")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	otherFile := filepath.Join(sessionsDir, "trace-test-session-123.jsonl")
	if err := os.WriteFile(otherFile, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	expected := filepath.Join(sessionsDir, "rollout-test-session-123.jsonl")
	if err := os.WriteFile(expected, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CODEX_HOME", codexHome)

	if got := findRolloutFile("test-session-123"); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestFindRolloutFileEmptySessionID(t *testing.T) {
	if got := findRolloutFile(""); got != "" {
		t.Fatalf("findRolloutFile(\"\") = %q, want empty", got)
	}
}

func TestFindRolloutFileNoMatch(t *testing.T) {
	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "04", "13")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(sessionsDir, "rollout-another-session.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CODEX_HOME", codexHome)

	if got := findRolloutFile("missing-session"); got != "" {
		t.Fatalf("findRolloutFile() = %q, want empty", got)
	}
}

func TestFindRolloutFileIgnoresNearMatch(t *testing.T) {
	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "04", "13")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionID := "test-session-123"
	nearMatch := filepath.Join(sessionsDir, "rollout-2026-04-13T08-00-00-"+sessionID+"-1.jsonl")
	if err := os.WriteFile(nearMatch, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	expected := filepath.Join(sessionsDir, "rollout-2026-04-13T08-01-00-"+sessionID+".jsonl")
	if err := os.WriteFile(expected, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CODEX_HOME", codexHome)

	if got := findRolloutFile(sessionID); got != expected {
		t.Fatalf("findRolloutFile() = %q, want %q", got, expected)
	}
}

func TestParseRolloutLine_AgentReasoning(t *testing.T) {
	line := []byte(`{"timestamp":"2026-03-15T06:17:13.231Z","type":"event_msg","payload":{"type":"agent_reasoning","text":"**Planning broader usage search**"}}`)

	parsed, err := parseRolloutLine(line, "session-1")
	if err != nil {
		t.Fatalf("parseRolloutLine() error = %v", err)
	}
	if parsed == nil || parsed.Done {
		t.Fatalf("parsed = %#v, want non-nil non-done", parsed)
	}
	if parsed.Event == nil || parsed.Event.Type != core.EventThinking || parsed.Event.Content != "**Planning broader usage search**" {
		t.Fatalf("unexpected parsed event: %#v", parsed)
	}
}

func TestParseRolloutLine_TaskComplete(t *testing.T) {
	line := []byte(`{"timestamp":"2026-03-15T06:18:14.689Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","last_agent_message":"finished"}}`)

	parsed, err := parseRolloutLine(line, "session-1")
	if err != nil {
		t.Fatalf("parseRolloutLine() error = %v", err)
	}
	if parsed == nil || !parsed.Done {
		t.Fatalf("parsed = %#v, want done", parsed)
	}
	if parsed.Event == nil || parsed.Event.Type != core.EventResult || !parsed.Event.Done || parsed.Event.Content != "finished" || parsed.Event.SessionID != "session-1" {
		t.Fatalf("unexpected parsed event: %#v", parsed)
	}
}

func TestObserveSessionAlreadyCompleteReturnsSentinel(t *testing.T) {
	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "03", "15")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	sessionID := "019cf022-2d7b-72b1-a1d6-5e4d60158f61"
	rolloutPath := filepath.Join(sessionsDir, "rollout-2026-03-15T13-15-49-"+sessionID+".jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-03-15T06:18:14.382Z","type":"event_msg","payload":{"type":"agent_message","message":"finished"}}`,
		`{"timestamp":"2026-03-15T06:18:14.689Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","last_agent_message":"finished"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(rolloutPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	t.Setenv("CODEX_HOME", codexHome)

	a := &Agent{workDir: tmpDir}
	_, err := a.ObserveSession(context.Background(), sessionID)
	if !errors.Is(err, core.ErrObservedSessionComplete) {
		t.Fatalf("ObserveSession() error = %v, want ErrObservedSessionComplete", err)
	}
}

func TestObserveSessionStartsFromEOFAndOnlyStreamsNewEvents(t *testing.T) {
	prevPoll := observePollInterval
	observePollInterval = 10 * time.Millisecond
	t.Cleanup(func() { observePollInterval = prevPoll })

	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "03", "15")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	sessionID := "019cf022-2d7b-72b1-a1d6-5e4d60158f62"
	rolloutPath := filepath.Join(sessionsDir, "rollout-2026-03-15T13-15-49-"+sessionID+".jsonl")
	initial := strings.Join([]string{
		`{"timestamp":"2026-03-15T06:18:00.000Z","type":"event_msg","payload":{"type":"agent_message","message":"old-message"}}`,
		`{"timestamp":"2026-03-15T06:18:01.000Z","type":"event_msg","payload":{"type":"agent_reasoning","text":"old-thinking"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(rolloutPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	t.Setenv("CODEX_HOME", codexHome)

	a := &Agent{workDir: tmpDir}
	obs, err := a.ObserveSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ObserveSession() error = %v", err)
	}
	t.Cleanup(func() { _ = obs.Close() })

	if obs.SourcePath() != rolloutPath {
		t.Fatalf("SourcePath() = %q, want %q", obs.SourcePath(), rolloutPath)
	}

	// Give the observe goroutine enough time to start tailing from EOF.
	time.Sleep(20 * time.Millisecond)

	appendLines := strings.Join([]string{
		`{"timestamp":"2026-03-15T06:18:02.000Z","type":"event_msg","payload":{"type":"agent_reasoning","text":"new-thinking"}}`,
		`{"timestamp":"2026-03-15T06:18:03.000Z","type":"response_item","payload":{"type":"function_call","name":"Bash","arguments":"echo hi","call_id":"call-1"}}`,
		`{"timestamp":"2026-03-15T06:18:04.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"hi"}}`,
		`{"timestamp":"2026-03-15T06:18:05.000Z","type":"event_msg","payload":{"type":"agent_message","message":"new-message"}}`,
		`{malformed-json`,
		`{"timestamp":"2026-03-15T06:18:06.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","last_agent_message":"done"}}`,
	}, "\n") + "\n"
	f, err := os.OpenFile(rolloutPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open rollout for append: %v", err)
	}
	if _, err := f.WriteString(appendLines); err != nil {
		_ = f.Close()
		t.Fatalf("append rollout lines: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close rollout file: %v", err)
	}

	got := make([]core.Event, 0, 8)
	timeout := time.After(2 * time.Second)
	for len(got) < 5 {
		select {
		case evt, ok := <-obs.Events():
			if !ok {
				t.Fatalf("events channel closed before receiving expected events: got=%d", len(got))
			}
			got = append(got, evt)
			if evt.Type == core.EventResult && evt.Done {
				goto CHECK
			}
		case <-timeout:
			t.Fatalf("timeout waiting for observed events, got=%d", len(got))
		}
	}

CHECK:
	if len(got) != 5 {
		t.Fatalf("observed event count = %d, want 5; events=%#v", len(got), got)
	}
	if got[0].Type != core.EventThinking || got[0].Content != "new-thinking" {
		t.Fatalf("unexpected first event: %#v", got[0])
	}
	if got[1].Type != core.EventToolUse || got[1].ToolName != "Bash" || got[1].ToolInput != "echo hi" {
		t.Fatalf("unexpected tool use event: %#v", got[1])
	}
	if got[2].Type != core.EventToolResult || got[2].ToolName != "Bash" || got[2].ToolResult != "hi" {
		t.Fatalf("unexpected tool result event: %#v", got[2])
	}
	if got[3].Type != core.EventText || got[3].Content != "new-message" || got[3].SessionID != sessionID {
		t.Fatalf("unexpected text event: %#v", got[3])
	}
	if got[4].Type != core.EventResult || !got[4].Done || got[4].Content != "done" || got[4].SessionID != sessionID {
		t.Fatalf("unexpected result event: %#v", got[4])
	}
}
