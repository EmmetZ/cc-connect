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

const rolloutTailScanBlockSize int64 = 64 * 1024

type observedRolloutSession struct {
	path      string
	sessionID string
	events    chan core.Event
	ctx       context.Context
	cancel    context.CancelFunc
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

	info, err := f.Stat()
	if err != nil {
		return false, err
	}

	offset := info.Size()
	tail := make([]byte, 0, rolloutTailScanBlockSize)
	for offset > 0 {
		readSize := rolloutTailScanBlockSize
		if readSize > offset {
			readSize = offset
		}
		offset -= readSize

		buf := make([]byte, readSize)
		if _, err := f.ReadAt(buf, offset); err != nil {
			return false, err
		}

		chunk := make([]byte, 0, len(buf)+len(tail))
		chunk = append(chunk, buf...)
		chunk = append(chunk, tail...)

		lines := bytes.Split(chunk, []byte{'\n'})
		if offset > 0 {
			tail = append(tail[:0], lines[0]...)
			lines = lines[1:]
		} else {
			tail = tail[:0]
		}

		for i := len(lines) - 1; i >= 0; i-- {
			if complete, decided := rolloutCompletionState(lines[i]); decided {
				return complete, nil
			}
		}
	}

	if len(tail) > 0 {
		if complete, decided := rolloutCompletionState(tail); decided {
			return complete, nil
		}
	}

	return false, nil
}

func rolloutCompletionState(line []byte) (complete bool, decided bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return false, false
	}

	var env rolloutEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return false, false
	}
	if env.Type != "event_msg" {
		return false, false
	}

	var payload struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return false, false
	}

	switch payload.Type {
	case "task_complete":
		return true, true
	case "task_started":
		return false, true
	default:
		return false, false
	}
}
