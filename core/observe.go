package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"
)

var observeGuardInterval = time.Second

type observeState struct {
	ctx          context.Context
	sessionID    string
	sessionKey   string
	sourcePath   string
	startedAt    time.Time
	platform     Platform
	replyCtx     any
	observer     ObservedSession
	cancel       context.CancelFunc
	workspaceDir string
}

func (e *Engine) getObserveState() *observeState {
	e.sessionObserveMu.Lock()
	defer e.sessionObserveMu.Unlock()

	if e.sessionObserveState == nil {
		return nil
	}
	cp := *e.sessionObserveState
	return &cp
}

func (e *Engine) takeObserveState(expected *observeState) *observeState {
	e.sessionObserveMu.Lock()
	defer e.sessionObserveMu.Unlock()

	current := e.sessionObserveState
	if current == nil {
		return nil
	}
	if expected != nil && current != expected {
		return nil
	}
	e.sessionObserveState = nil
	return current
}

func (e *Engine) stopObservedSession(expected *observeState) {
	state := e.takeObserveState(expected)
	if state == nil {
		return
	}

	if state.cancel != nil {
		state.cancel()
	}
	if state.observer != nil {
		if err := state.observer.Close(); err != nil {
			slog.Warn("observe: close failed", "session_id", state.sessionID, "error", err)
		}
	}
}

func (e *Engine) observeCommandContext(p Platform, msg *Message) (Agent, *SessionManager, string, error) {
	if !e.multiWorkspace {
		return e.agent, e.sessions, "", nil
	}

	channelID := effectiveChannelID(msg)
	channelKey := effectiveWorkspaceChannelKey(msg)
	if channelID == "" || channelKey == "" {
		return e.agent, e.sessions, "", nil
	}

	workspace, _, err := e.resolveWorkspace(p, channelID)
	if err != nil {
		return nil, nil, "", err
	}
	if workspace == "" {
		return e.agent, e.sessions, "", nil
	}

	agent, sessions, _, effectiveDir, err := e.workspaceContext(workspace, msg.SessionKey)
	if err != nil {
		return nil, nil, "", err
	}
	return agent, sessions, effectiveDir, nil
}

func (e *Engine) cmdObserve(p Platform, msg *Message, args []string) {
	if len(args) > 0 {
		switch matchSubCommand(strings.ToLower(strings.TrimSpace(args[0])), []string{"status", "stop"}) {
		case "status":
			e.cmdObserveStatus(p, msg)
			return
		case "stop":
			e.cmdObserveStop(p, msg)
			return
		default:
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgObserveUsage))
			return
		}
	}

	e.cmdObserveStart(p, msg)
}

func (e *Engine) cmdObserveStart(p Platform, msg *Message) {
	agent, sessions, workspaceDir, err := e.observeCommandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}

	observer, ok := agent.(SessionObserver)
	if !ok {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgObserveUnsupported))
		return
	}

	session := sessions.GetOrCreateActive(msg.SessionKey)
	sessionID := strings.TrimSpace(session.GetAgentSessionID())
	if sessionID == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgNameNoSession))
		return
	}

	if active := e.getObserveState(); active != nil && active.sessionID == sessionID {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgObserveAlready, sessionID))
		return
	}

	// Replacement is one-way: if the new observe start fails, we keep no observer.
	e.stopObservedSession(nil)

	ctx, cancel := context.WithCancel(e.ctx)
	observed, err := observer.ObserveSession(ctx, sessionID)
	if err != nil {
		cancel()
		if errors.Is(err, ErrObservedSessionComplete) {
			e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgObserveAlreadyComplete, sessionID))
			return
		}
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgError, err))
		return
	}

	state := &observeState{
		ctx:          ctx,
		sessionID:    sessionID,
		sessionKey:   msg.SessionKey,
		sourcePath:   observed.SourcePath(),
		startedAt:    time.Now(),
		platform:     p,
		replyCtx:     msg.ReplyCtx,
		observer:     observed,
		cancel:       cancel,
		workspaceDir: workspaceDir,
	}

	e.sessionObserveMu.Lock()
	e.sessionObserveState = state
	e.sessionObserveMu.Unlock()

	go e.runObservedSession(state, sessions)

	e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgObserveStarted, state.sessionID, state.sourcePath))
}

func (e *Engine) cmdObserveStatus(p Platform, msg *Message) {
	state := e.getObserveState()
	if state == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgObserveStatusInactive))
		return
	}

	e.reply(p, msg.ReplyCtx, e.i18n.Tf(
		MsgObserveStatusActive,
		state.sessionID,
		state.sourcePath,
		state.sessionKey,
		state.startedAt.Format(time.RFC3339),
	))
}

func (e *Engine) cmdObserveStop(p Platform, msg *Message) {
	if e.getObserveState() == nil {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgObserveStatusInactive))
		return
	}

	e.stopObservedSession(nil)
	e.reply(p, msg.ReplyCtx, e.i18n.T(MsgObserveStopped))
}

func (e *Engine) runObservedSession(state *observeState, sessions *SessionManager) {
	defer e.stopObservedSession(state)

	render := func(content string) string {
		return e.renderOutgoingContentForWorkspace(state.platform, content, state.workspaceDir)
	}
	sendObserved := func(content string) error {
		return e.sendWithErrorForWorkspace(state.platform, state.replyCtx, content, state.workspaceDir)
	}

	sp := newStreamPreview(e.streamPreview, state.platform, state.replyCtx, e.ctx, render)
	cp := newCompactProgressWriter(e.ctx, state.platform, state.replyCtx, e.agent.Name(), e.i18n.CurrentLang(), render)

	var textParts []string
	var segmentStart int
	toolCount := 0

	ticker := time.NewTicker(observeGuardInterval)
	defer ticker.Stop()

	events := state.observer.Events()
	for {
		var event Event
		var ok bool

		select {
		case <-state.ctx.Done():
			sp.discard()
			return
		case <-e.ctx.Done():
			sp.discard()
			return
		case <-ticker.C:
			current := strings.TrimSpace(sessions.GetOrCreateActive(state.sessionKey).GetAgentSessionID())
			if current != state.sessionID {
				sp.discard()
				return
			}
			continue
		case event, ok = <-events:
			if !ok {
				sp.discard()
				return
			}
		}

		if state.ctx.Err() != nil {
			sp.discard()
			return
		}

		switch event.Type {
		case EventThinking:
			if e.display.ThinkingMessages && event.Content != "" {
				previewActive := sp.canPreview()
				if len(textParts) > segmentStart {
					if !previewActive {
						segment := strings.Join(textParts[segmentStart:], "")
						if segment != "" {
							for _, chunk := range splitMessage(segment, maxPlatformMessageLen) {
								if err := sendObserved(chunk); err != nil {
									return
								}
							}
						}
					}
					segmentStart = len(textParts)
				}

				sp.freeze()
				if previewActive {
					sp.detachPreview()
				}

				preview := truncateIf(event.Content, e.display.ThinkingMaxLen)
				thinkingMsg := fmt.Sprintf(e.i18n.T(MsgThinking), preview)
				if !cp.AppendEvent(ProgressEntryThinking, preview, "", thinkingMsg) {
					if err := sendObserved(thinkingMsg); err != nil {
						return
					}
				}
			}

		case EventToolUse:
			toolCount++
			if e.display.ToolMessages {
				previewActive := sp.canPreview()
				if len(textParts) > segmentStart {
					if !previewActive {
						segment := strings.Join(textParts[segmentStart:], "")
						if segment != "" {
							for _, chunk := range splitMessage(segment, maxPlatformMessageLen) {
								if err := sendObserved(chunk); err != nil {
									return
								}
							}
						}
					}
					segmentStart = len(textParts)
				}

				sp.freeze()
				if previewActive {
					sp.detachPreview()
				}

				toolInput := event.ToolInput
				var formattedInput string
				switch {
				case toolInput == "":
					formattedInput = ""
				case strings.Contains(toolInput, "```"):
					formattedInput = toolInput
				case strings.Contains(toolInput, "\n") || utf8.RuneCountInString(toolInput) > 200:
					formattedInput = fmt.Sprintf("```%s\n%s\n```", toolCodeLang(event.ToolName, toolInput), toolInput)
				default:
					switch event.ToolName {
					case "shell", "run_shell_command", "Bash":
						formattedInput = fmt.Sprintf("```bash\n%s\n```", toolInput)
					default:
						formattedInput = fmt.Sprintf("`%s`", toolInput)
					}
				}

				toolMsg := fmt.Sprintf(e.i18n.T(MsgTool), toolCount, event.ToolName, formattedInput)
				if !cp.AppendEvent(ProgressEntryToolUse, toolInput, event.ToolName, toolMsg) {
					for _, chunk := range SplitMessageCodeFenceAware(toolMsg, maxPlatformMessageLen) {
						if err := sendObserved(chunk); err != nil {
							return
						}
					}
				}
			}

		case EventToolResult:
			if e.display.ToolMessages {
				result := strings.TrimSpace(event.ToolResult)
				if result == "" {
					result = strings.TrimSpace(event.Content)
				}
				if result != "" {
					result = truncateIf(result, e.display.ToolMaxLen)
				}
				if result != "" || event.ToolStatus != "" || event.ToolExitCode != nil || event.ToolSuccess != nil {
					resultMsg := e.formatToolResultEventFallback(event.ToolName, result, event.ToolStatus, event.ToolExitCode, event.ToolSuccess)
					entry := ProgressCardEntry{
						Kind:     ProgressEntryToolResult,
						Tool:     event.ToolName,
						Text:     result,
						Status:   event.ToolStatus,
						ExitCode: event.ToolExitCode,
						Success:  event.ToolSuccess,
					}
					if !cp.AppendStructured(entry, resultMsg) && !SuppressStandaloneToolResultEvent(state.platform) {
						e.sendRaw(state.platform, state.replyCtx, resultMsg)
					}
				}
			}

		case EventText:
			if event.Content != "" {
				textParts = append(textParts, event.Content)
				if sp.canPreview() {
					sp.appendText(event.Content)
				}
			}

		case EventResult:
			cp.Finalize(ProgressCardStateCompleted)

			fullResponse := event.Content
			if fullResponse == "" && len(textParts) > 0 {
				fullResponse = strings.Join(textParts, "")
			}
			if fullResponse == "" {
				fullResponse = e.i18n.T(MsgEmptyResponse)
			}

			if toolCount > 0 && segmentStart > 0 {
				sp.discard()
				if segmentStart < len(textParts) {
					unsent := strings.Join(textParts[segmentStart:], "")
					if unsent != "" {
						for _, chunk := range splitMessage(unsent, maxPlatformMessageLen) {
							if err := sendObserved(chunk); err != nil {
								return
							}
						}
					}
				}
			} else if sp.finish(fullResponse) {
				slog.Debug("observe: finalized via stream preview", "response_len", len(fullResponse), "session_id", state.sessionID)
			} else {
				for _, chunk := range splitMessage(fullResponse, maxPlatformMessageLen) {
					if err := sendObserved(chunk); err != nil {
						return
					}
				}
			}
			return

		case EventError:
			cp.Finalize(ProgressCardStateFailed)
			sp.discard()
			if event.Error != nil {
				e.send(state.platform, state.replyCtx, fmt.Sprintf(e.i18n.T(MsgError), event.Error))
			}
			return
		}
	}
}
