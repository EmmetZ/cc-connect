package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.SessionObserver = (*Agent)(nil)

var observePollInterval = 300 * time.Millisecond

type observedRolloutSession struct {
	path      string
	sessionID string
	events    chan core.Event
	ctx       context.Context
	cancel    context.CancelFunc
	toolCalls map[string]string
	wg        sync.WaitGroup
	closeOnce sync.Once
}

func (a *Agent) ObserveSession(ctx context.Context, sessionID string) (core.ObservedSession, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("codex observe: session id is required")
	}

	path := findRolloutFile(sessionID)
	if path == "" {
		return nil, fmt.Errorf("codex observe: rollout file not found for session %s", sessionID)
	}

	complete, err := rolloutAlreadyComplete(path)
	if err != nil {
		return nil, fmt.Errorf("codex observe: inspect rollout tail: %w", err)
	}
	if complete {
		return nil, core.ErrObservedSessionComplete
	}

	return newObservedRolloutSession(ctx, path, sessionID)
}

func newObservedRolloutSession(parent context.Context, path, sessionID string) (*observedRolloutSession, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat rollout: %w", err)
	}

	ctx, cancel := context.WithCancel(parent)
	s := &observedRolloutSession{
		path:      path,
		sessionID: sessionID,
		events:    make(chan core.Event, 64),
		ctx:       ctx,
		cancel:    cancel,
		toolCalls: make(map[string]string),
	}

	s.wg.Add(1)
	go s.pollLoop(info.Size())
	return s, nil
}

func (s *observedRolloutSession) Events() <-chan core.Event {
	return s.events
}

func (s *observedRolloutSession) SourcePath() string {
	return s.path
}

func (s *observedRolloutSession) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.wg.Wait()
	})
	return nil
}

func (s *observedRolloutSession) pollLoop(offset int64) {
	defer s.wg.Done()
	defer close(s.events)

	ticker := time.NewTicker(observePollInterval)
	defer ticker.Stop()

	for {
		nextOffset, done := s.pollOnce(offset)
		offset = nextOffset
		if done {
			return
		}

		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *observedRolloutSession) pollOnce(offset int64) (int64, bool) {
	f, err := os.Open(s.path)
	if err != nil {
		slog.Warn("codex observe: open rollout file failed", "path", s.path, "error", err)
		return offset, false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		slog.Warn("codex observe: stat rollout file failed", "path", s.path, "error", err)
		return offset, false
	}
	if info.Size() < offset {
		offset = info.Size()
	}
	if info.Size() == offset {
		return offset, false
	}

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		slog.Warn("codex observe: seek rollout file failed", "path", s.path, "error", err)
		return offset, false
	}

	reader := bufio.NewReader(f)
	current := offset
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			// Incomplete final line is intentionally retried next poll.
			return current, false
		}
		if err != nil {
			slog.Warn("codex observe: read rollout line failed", "path", s.path, "error", err)
			return current, false
		}

		current += int64(len(line))
		line = bytes.TrimRight(line, "\r\n")
		if len(line) == 0 {
			continue
		}

		parsed, err := parseRolloutLine(line, s.sessionID)
		if err != nil {
			slog.Warn("codex observe: skip malformed rollout line", "path", s.path, "error", err)
			continue
		}

		if s.handleParsed(parsed) {
			return current, true
		}
	}
}

func (s *observedRolloutSession) handleParsed(parsed *parsedRolloutEvent) bool {
	if parsed == nil || parsed.Event == nil {
		return false
	}

	if parsed.CallID != "" && parsed.ToolName != "" {
		s.toolCalls[parsed.CallID] = parsed.ToolName
	}

	if parsed.Event.Type == core.EventToolResult && parsed.CallID != "" {
		if parsed.Event.ToolName == "" {
			parsed.Event.ToolName = s.toolCalls[parsed.CallID]
		}
		delete(s.toolCalls, parsed.CallID)
	}

	select {
	case <-s.ctx.Done():
		return true
	case s.events <- *parsed.Event:
		return parsed.Done
	}
}

func rolloutAlreadyComplete(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 128*1024), 2*1024*1024)

	complete := false
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var env rolloutEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		if env.Type != "event_msg" {
			continue
		}

		var payload struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			continue
		}

		switch payload.Type {
		case "task_started":
			complete = false
		case "task_complete":
			complete = true
		}
	}

	if err := scanner.Err(); err != nil {
		return false, err
	}
	return complete, nil
}
