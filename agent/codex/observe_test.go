package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func mustRolloutLine(t *testing.T, envType string, payload map[string]any) []byte {
	t.Helper()

	line, err := json.Marshal(map[string]any{
		"type":    envType,
		"payload": payload,
	})
	if err != nil {
		t.Fatalf("marshal rollout line: %v", err)
	}
	return line
}

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

func TestParseRolloutLine_AgentMessageCommentaryBecomesThinking(t *testing.T) {
	line := mustRolloutLine(t, "event_msg", map[string]any{
		"type":    "agent_message",
		"message": "working on it",
		"phase":   "commentary",
	})

	parsed, err := parseRolloutLine(line, "session-1")
	if err != nil {
		t.Fatalf("parseRolloutLine() error = %v", err)
	}
	if parsed == nil || parsed.Event == nil {
		t.Fatalf("parsed = %#v, want non-nil event", parsed)
	}
	if parsed.Event.Type != core.EventThinking || parsed.Event.Content != "working on it" {
		t.Fatalf("unexpected parsed event: %#v", parsed.Event)
	}
}

func TestParseRolloutLine_AgentMessageFinalAnswerBecomesText(t *testing.T) {
	line := mustRolloutLine(t, "event_msg", map[string]any{
		"type":    "agent_message",
		"message": "done",
		"phase":   "final_answer",
	})

	parsed, err := parseRolloutLine(line, "session-1")
	if err != nil {
		t.Fatalf("parseRolloutLine() error = %v", err)
	}
	if parsed == nil || parsed.Event == nil {
		t.Fatalf("parsed = %#v, want non-nil event", parsed)
	}
	if parsed.Event.Type != core.EventText || parsed.Event.Content != "done" || parsed.Event.SessionID != "session-1" {
		t.Fatalf("unexpected parsed event: %#v", parsed.Event)
	}
}

func TestParseRolloutLine_ResponseItemAssistantMessageCommentaryBecomesThinking(t *testing.T) {
	line := mustRolloutLine(t, "response_item", map[string]any{
		"type":  "message",
		"role":  "assistant",
		"phase": "commentary",
		"content": []map[string]any{
			{
				"type": "output_text",
				"text": "working on it",
			},
		},
	})

	parsed, err := parseRolloutLine(line, "session-1")
	if err != nil {
		t.Fatalf("parseRolloutLine() error = %v", err)
	}
	if parsed == nil || parsed.Event == nil {
		t.Fatalf("parsed = %#v, want non-nil event", parsed)
	}
	if parsed.Event.Type != core.EventThinking || parsed.Event.Content != "working on it" {
		t.Fatalf("unexpected parsed event: %#v", parsed.Event)
	}
}

func TestParseRolloutLine_ResponseItemAssistantMessageFinalAnswerBecomesText(t *testing.T) {
	line := mustRolloutLine(t, "response_item", map[string]any{
		"type":  "message",
		"role":  "assistant",
		"phase": "final_answer",
		"content": []map[string]any{
			{
				"type": "output_text",
				"text": "done",
			},
		},
	})

	parsed, err := parseRolloutLine(line, "session-1")
	if err != nil {
		t.Fatalf("parseRolloutLine() error = %v", err)
	}
	if parsed == nil || parsed.Event == nil {
		t.Fatalf("parsed = %#v, want non-nil event", parsed)
	}
	if parsed.Event.Type != core.EventText || parsed.Event.Content != "done" || parsed.Event.SessionID != "session-1" {
		t.Fatalf("unexpected parsed event: %#v", parsed.Event)
	}
}

func TestParseRolloutLine_ResponseItemNonAssistantMessageIgnored(t *testing.T) {
	line := mustRolloutLine(t, "response_item", map[string]any{
		"type": "message",
		"role": "user",
		"content": []map[string]any{
			{
				"type": "input_text",
				"text": "question",
			},
		},
	})

	parsed, err := parseRolloutLine(line, "session-1")
	if err != nil {
		t.Fatalf("parseRolloutLine() error = %v", err)
	}
	if parsed != nil {
		t.Fatalf("parsed = %#v, want nil", parsed)
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

func TestParseRolloutLine_ExecCommandFormatsAsBashToolUse(t *testing.T) {
	line := mustRolloutLine(t, "response_item", map[string]any{
		"type":      "function_call",
		"name":      "exec_command",
		"call_id":   "call-1",
		"arguments": `{"cmd":"echo hi","workdir":"/tmp","yield_time_ms":1000}`,
	})

	parsed, err := parseRolloutLine(line, "session-1")
	if err != nil {
		t.Fatalf("parseRolloutLine() error = %v", err)
	}
	if parsed == nil || parsed.Event == nil {
		t.Fatalf("parsed = %#v, want non-nil event", parsed)
	}
	if parsed.Event.Type != core.EventToolUse {
		t.Fatalf("event type = %v, want %v", parsed.Event.Type, core.EventToolUse)
	}
	if parsed.Event.ToolName != "Bash" || parsed.Event.ToolInput != "echo hi" {
		t.Fatalf("unexpected tool use event: %#v", parsed.Event)
	}
}

func TestParseRolloutLine_ExecCommandEndBecomesToolResult(t *testing.T) {
	line := mustRolloutLine(t, "event_msg", map[string]any{
		"type":              "exec_command_end",
		"call_id":           "call-1",
		"aggregated_output": "hi\n",
		"exit_code":         0,
		"status":            "completed",
	})

	parsed, err := parseRolloutLine(line, "session-1")
	if err != nil {
		t.Fatalf("parseRolloutLine() error = %v", err)
	}
	if parsed == nil || parsed.Event == nil {
		t.Fatalf("parsed = %#v, want non-nil event", parsed)
	}
	if parsed.Event.Type != core.EventToolResult {
		t.Fatalf("event type = %v, want %v", parsed.Event.Type, core.EventToolResult)
	}
	if parsed.Event.ToolName != "Bash" || parsed.Event.ToolResult != "hi" {
		t.Fatalf("unexpected tool result event: %#v", parsed.Event)
	}
	if parsed.Event.ToolExitCode == nil || *parsed.Event.ToolExitCode != 0 {
		t.Fatalf("tool exit code = %#v, want 0", parsed.Event.ToolExitCode)
	}
	if parsed.Event.ToolSuccess == nil || !*parsed.Event.ToolSuccess {
		t.Fatalf("tool success = %#v, want true", parsed.Event.ToolSuccess)
	}
}

func TestParseRolloutLine_FunctionCallOutputIgnored(t *testing.T) {
	line := mustRolloutLine(t, "response_item", map[string]any{
		"type":    "function_call_output",
		"call_id": "call-1",
		"output":  strings.Repeat("x", 600),
	})

	parsed, err := parseRolloutLine(line, "session-1")
	if err != nil {
		t.Fatalf("parseRolloutLine() error = %v", err)
	}
	if parsed != nil {
		t.Fatalf("parsed = %#v, want nil", parsed)
	}
}

func TestParseRolloutLine_CustomToolCallIgnored(t *testing.T) {
	line := mustRolloutLine(t, "response_item", map[string]any{
		"type":    "custom_tool_call",
		"name":    "exec_command",
		"call_id": "call-1",
		"input": map[string]any{
			"cmd": "echo hi",
		},
	})

	parsed, err := parseRolloutLine(line, "session-1")
	if err != nil {
		t.Fatalf("parseRolloutLine() error = %v", err)
	}
	if parsed != nil {
		t.Fatalf("parsed = %#v, want nil", parsed)
	}
}

func TestParseRolloutLine_CustomToolCallOutputIgnored(t *testing.T) {
	line := mustRolloutLine(t, "response_item", map[string]any{
		"type":    "custom_tool_call_output",
		"call_id": "call-1",
		"output":  "Plan updated",
	})

	parsed, err := parseRolloutLine(line, "session-1")
	if err != nil {
		t.Fatalf("parseRolloutLine() error = %v", err)
	}
	if parsed != nil {
		t.Fatalf("parsed = %#v, want nil", parsed)
	}
}

func TestParseRolloutLine_WebSearchCallEmitsToolUse(t *testing.T) {
	line := mustRolloutLine(t, "response_item", map[string]any{
		"type":   "web_search_call",
		"status": "completed",
		"action": map[string]any{
			"type":    "search",
			"query":   "extended_nested_scroll_view pub.dev latest version",
			"queries": []string{"extended_nested_scroll_view pub.dev latest version", "pub.dev packages extended_nested_scroll_view"},
		},
	})

	parsed, err := parseRolloutLine(line, "session-1")
	if err != nil {
		t.Fatalf("parseRolloutLine() error = %v", err)
	}
	if parsed == nil || parsed.Event == nil {
		t.Fatalf("parsed = %#v, want non-nil event", parsed)
	}
	if parsed.Event.Type != core.EventToolUse {
		t.Fatalf("event type = %v, want %v", parsed.Event.Type, core.EventToolUse)
	}
	want := "extended_nested_scroll_view pub.dev latest version\npub.dev packages extended_nested_scroll_view"
	if parsed.Event.ToolName != "WebSearch" || parsed.Event.ToolInput != want {
		t.Fatalf("unexpected web search event: %#v", parsed.Event)
	}
}

func TestParseRolloutLine_KnownSessionToolCallsEmitToolUse(t *testing.T) {
	tests := []struct {
		name      string
		payload   map[string]any
		wantName  string
		wantInput string
	}{
		{
			name: "file search",
			payload: map[string]any{
				"type":  "file_search_call",
				"query": "observe.go",
			},
			wantName:  "FileSearch",
			wantInput: "observe.go",
		},
		{
			name: "code interpreter",
			payload: map[string]any{
				"type": "code_interpreter_call",
				"name": "python",
			},
			wantName:  "CodeInterpreter",
			wantInput: "python",
		},
		{
			name: "computer use",
			payload: map[string]any{
				"type": "computer_use_call",
				"name": "click",
			},
			wantName:  "ComputerUse",
			wantInput: "click",
		},
		{
			name: "mcp tool",
			payload: map[string]any{
				"type": "mcp_tool_call",
				"name": "list_tables",
			},
			wantName:  "MCP",
			wantInput: "list_tables",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := mustRolloutLine(t, "response_item", tt.payload)

			parsed, err := parseRolloutLine(line, "session-1")
			if err != nil {
				t.Fatalf("parseRolloutLine() error = %v", err)
			}
			if parsed == nil || parsed.Event == nil {
				t.Fatalf("parsed = %#v, want non-nil event", parsed)
			}
			if parsed.Event.Type != core.EventToolUse {
				t.Fatalf("event type = %v, want %v", parsed.Event.Type, core.EventToolUse)
			}
			if parsed.Event.ToolName != tt.wantName || parsed.Event.ToolInput != tt.wantInput {
				t.Fatalf("unexpected tool use event: %#v", parsed.Event)
			}
		})
	}
}

func TestParseRolloutLine_EventUserMessageIgnored(t *testing.T) {
	line := mustRolloutLine(t, "event_msg", map[string]any{
		"type":    "user_message",
		"message": "hi",
	})

	parsed, err := parseRolloutLine(line, "session-1")
	if err != nil {
		t.Fatalf("parseRolloutLine() error = %v", err)
	}
	if parsed != nil {
		t.Fatalf("parsed = %#v, want nil", parsed)
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

func TestRolloutAlreadyCompleteReturnsFalseWhenLatestLifecycleEventIsTaskStarted(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "rollout.jsonl")
	content := strings.Join([]string{
		`{"timestamp":"2026-03-15T06:18:14.689Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","last_agent_message":"finished"}}`,
		`{"timestamp":"2026-03-15T06:19:00.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-2"}}`,
		`{"timestamp":"2026-03-15T06:19:01.000Z","type":"event_msg","payload":{"type":"agent_message","message":"still running"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	complete, err := rolloutAlreadyComplete(path)
	if err != nil {
		t.Fatalf("rolloutAlreadyComplete() error = %v", err)
	}
	if complete {
		t.Fatal("rolloutAlreadyComplete() = true, want false")
	}
}

func TestRolloutAlreadyCompleteSkipsHugeHistoricalPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "rollout.jsonl")
	hugeLine := strings.Repeat("x", 3*1024*1024)
	content := strings.Join([]string{
		hugeLine,
		`{"timestamp":"2026-03-15T06:18:00.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
		`{"timestamp":"2026-03-15T06:18:14.689Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","last_agent_message":"finished"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	complete, err := rolloutAlreadyComplete(path)
	if err != nil {
		t.Fatalf("rolloutAlreadyComplete() error = %v", err)
	}
	if !complete {
		t.Fatal("rolloutAlreadyComplete() = false, want true")
	}
}

func TestObserveSessionTransformsCommentaryAndExecCommandEvents(t *testing.T) {
	prevPoll := observePollInterval
	observePollInterval = 10 * time.Millisecond
	t.Cleanup(func() { observePollInterval = prevPoll })

	tmpDir := t.TempDir()
	codexHome := filepath.Join(tmpDir, ".codex")
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "03", "15")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	sessionID := "019cf022-2d7b-72b1-a1d6-5e4d60158f63"
	rolloutPath := filepath.Join(sessionsDir, "rollout-2026-03-15T13-15-49-"+sessionID+".jsonl")
	if err := os.WriteFile(rolloutPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	t.Setenv("CODEX_HOME", codexHome)

	a := &Agent{workDir: tmpDir}
	obs, err := a.ObserveSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ObserveSession() error = %v", err)
	}
	t.Cleanup(func() { _ = obs.Close() })

	time.Sleep(20 * time.Millisecond)

	appendLines := strings.Join([]string{
		`{"timestamp":"2026-03-15T06:18:02.000Z","type":"event_msg","payload":{"type":"agent_message","message":"planning","phase":"commentary"}}`,
		`{"timestamp":"2026-03-15T06:18:03.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"echo hi\",\"workdir\":\"/tmp\"}","call_id":"call-1"}}`,
		`{"timestamp":"2026-03-15T06:18:04.000Z","type":"event_msg","payload":{"type":"exec_command_end","call_id":"call-1","command":["/bin/zsh","-lc","echo hi"],"aggregated_output":"hi\n","exit_code":0,"status":"completed"}}`,
		`{"timestamp":"2026-03-15T06:18:05.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"Chunk ID: abc123\nWall time: 0.0000 seconds\nProcess exited with code 0\nOriginal token count: 2\nOutput:\nhi\n"}}`,
		`{"timestamp":"2026-03-15T06:18:06.000Z","type":"event_msg","payload":{"type":"agent_message","message":"done","phase":"final_answer"}}`,
		`{"timestamp":"2026-03-15T06:18:07.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","last_agent_message":"done"}}`,
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
	if got[0].Type != core.EventThinking || got[0].Content != "planning" {
		t.Fatalf("unexpected first event: %#v", got[0])
	}
	if got[1].Type != core.EventToolUse || got[1].ToolName != "Bash" || got[1].ToolInput != "echo hi" {
		t.Fatalf("unexpected tool use event: %#v", got[1])
	}
	if got[2].Type != core.EventToolResult || got[2].ToolName != "Bash" || got[2].ToolResult != "hi" {
		t.Fatalf("unexpected tool result event: %#v", got[2])
	}
	if got[3].Type != core.EventText || got[3].Content != "done" || got[3].SessionID != sessionID {
		t.Fatalf("unexpected text event: %#v", got[3])
	}
	if got[4].Type != core.EventResult || !got[4].Done || got[4].Content != "done" || got[4].SessionID != sessionID {
		t.Fatalf("unexpected result event: %#v", got[4])
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
		`{"timestamp":"2026-03-15T06:18:04.000Z","type":"event_msg","payload":{"type":"exec_command_end","call_id":"call-1","aggregated_output":"hi\n","exit_code":0,"status":"completed"}}`,
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
