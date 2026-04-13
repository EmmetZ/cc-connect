# Codex Observe for Telegram Design

**Date:** 2026-04-13
**Status:** Approved

## Problem

`cc-connect` can resume Codex sessions that it owns, but it cannot observe an already-running Codex task from Telegram. The target workflow is:

1. The user starts or continues a Codex task on the desktop
2. The user opens Telegram and runs `/current` to confirm the active Codex session
3. The user runs `/observe` to watch the ongoing task from the phone
4. Telegram receives the same style of thinking / tool / result updates that a normal Telegram-started task would produce
5. Observation stops automatically when the task is complete, or manually via `/observe stop`

The design must respect existing project rules:

- `core/` must not hardcode Codex-specific file names or payload schemas
- Telegram should remain a normal platform consumer; platform-specific rendering should reuse the existing event pipeline
- One bot must observe at most one Codex session at a time

## Goals

- Add `/observe`, `/observe status`, and `/observe stop`
- `/observe` always targets the current active session reported by `/current`
- Support observing Codex sessions by tailing `rollout-<sessionID>.jsonl`
- Reuse the existing `core.Event` -> Telegram rendering logic as much as possible
- Automatically stop observing when the rollout stream reports task completion
- Do not replay historical output; only forward content appended after observation starts

## Non-Goals

- Supporting multiple concurrent observe jobs per bot
- Supporting non-Codex agents in the first iteration
- Forwarding desktop-side user prompts into Telegram during observe mode
- Attaching to a live Codex process over stdio or RPC
- Building a generic web UI or management API for observation in this iteration

## User Experience

### Command semantics

- `/observe`
  - Starts observing the active Codex session for the current project / chat context
  - Before starting, inspect the tail of the rollout file; if the latest terminal event is already `task_complete`, do not start observation and return a concise "task already complete" message
  - If another session is already being observed, stop it first and replace it
  - If the same session is already being observed, return a concise "already observing" message
- `/observe status`
  - Shows whether observation is active
  - If active, include observed session ID, source file path, owning `session_key`, and start time
- `/observe stop`
  - Stops the current observer immediately
  - If no observer is active, return a concise "not observing" message

### Preconditions

`/observe` fails fast when any of the following is true:

- the current project agent is not Codex
- the current active session has no `agentSessionID`
- the corresponding rollout file cannot be found
- the agent does not implement the observe capability

### Output behavior

- Only newly appended rollout events are forwarded
- Desktop-side user prompts are not forwarded
- Assistant thinking, tool activity, tool results, and final response should look the same as the existing Telegram interactive flow as far as the current `core.Event` pipeline allows
- Observe completion should emit a normal final result, then stop automatically

### Active-session change behavior

Observation is pinned to the session that was active when `/observe` was issued.

- If the active session later changes in the same chat context, observation must stop automatically
- Observation must not silently retarget to the new current session
- The user must run `/observe` again if they want to observe the newly active session

In practice this should apply to any command or workflow that changes what `/current` would show, such as `/switch`, `/new`, or an automatic active-session reset.

## Architecture

### 1. Add an optional observe capability in `core`

`core` should define a capability-based contract instead of hardcoding Codex behavior.

Recommended interface shape:

```go
type SessionObserver interface {
    ObserveSession(ctx context.Context, sessionID string) (ObservedSession, error)
}

type ObservedSession interface {
    Events() <-chan Event
    SourcePath() string
    Close() error
}
```

Semantics:

- `ObserveSession` opens an append-only observation stream for an existing agent-managed session
- The returned event stream uses the same `core.Event` model already consumed by the engine
- `SourcePath()` is informational for `/observe status`
- `Close()` stops file tailing and releases resources

This preserves the project rule that `core/` knows only capabilities, not agent names, file layouts, or payload schemas.

### 2. Implement Codex rollout observation in `agent/codex`

Codex will implement `SessionObserver`.

Responsibilities:

- Resolve the session file path as `rollout-<sessionID>.jsonl` under the Codex sessions directory
- Seek to end-of-file on startup so only new events are observed
- Poll for incremental file growth, similar to the existing observer pattern in `core/observer.go`
- Parse appended JSONL entries into `core.Event`
- Stop automatically when a rollout entry reports `payload.type == "task_complete"`

Recommended file split:

- `agent/codex/observe.go`
  - observe capability implementation
  - file resolution
  - polling loop
  - lifecycle management
- `agent/codex/observe_parse.go`
  - rollout JSONL decoding
  - mapping from rollout payloads to `core.Event`
- tests next to those files

This keeps session execution logic in `agent/codex/session.go` separate from external session observation.

### 3. Add observe state management in `core.Engine`

`core.Engine` should own one global observe slot.

Recommended state:

```go
type observeState struct {
    sessionID   string
    sessionKey  string
    sourcePath  string
    startedAt   time.Time
    platform    Platform
    replyCtx    any
    observer    ObservedSession
    cancel      context.CancelFunc
}
```

Engine fields:

- a mutex dedicated to observe state
- a nullable pointer to the active `observeState`

Behavior:

- starting observation replaces any existing observe state
- stopping observation calls `cancel`, closes the observer, clears the state
- auto-stop on completion uses the same cleanup path as manual stop
- if the owning chat context changes its active session away from the observed `sessionID`, stop observation instead of rebinding it

This keeps lifecycle ownership clear and avoids leaked pollers.

### 4. Reuse the existing event rendering pipeline

The observe loop should not invent Telegram-specific formatting.

Instead, `core.Engine` should add a dedicated helper that consumes an `ObservedSession` event stream and routes events through the same event-to-message logic currently used by `processInteractiveMessageWith`.

The helper should reuse:

- thinking message display
- tool-use formatting
- tool-result formatting
- stream preview where applicable
- final result handling

It must not:

- mutate normal interactive session state
- enqueue new prompts
- wait for permission replies
- append observed output into the active chat session history

Observe mode is read-only monitoring, not a live interactive turn.

## Event Mapping

The Codex observe parser should map rollout payloads into the existing `core.Event` model.

Expected mappings:

- reasoning summary / reasoning text -> `EventThinking`
- tool invocation start -> `EventToolUse`
- tool completion -> `EventToolResult`
- assistant textual output -> `EventText`
- task completion -> `EventResult{Done: true}`
- rollout failure / explicit error payload -> `EventError`

Additional rules:

- user prompts are ignored
- empty text fragments are ignored
- repeated or partial fragments should be deduplicated or coalesced conservatively inside the parser if rollout entries emit incremental text
- `EventResult.Content` should contain the final assistant response when the rollout schema provides it; otherwise the engine may finalize from accumulated `EventText` content, matching existing behavior

## Automatic Stop Semantics

Observation should end automatically only when the rollout stream explicitly reports completion:

- if a parsed entry has `payload.type == "task_complete"`, emit `EventResult{Done: true}`
- after the final result is flushed to Telegram, stop the observer and clear engine state

Manual stop remains available via `/observe stop`.

No idle-time heuristic is needed in the first version because `task_complete` is the authoritative completion marker for the target workflow.

Startup should also short-circuit cleanly:

- if the latest relevant rollout entry already indicates `task_complete` before observation begins, do not start the observer
- return an informational message instead of an error, because the session state is valid but already finished

## Error Handling

### Start failures

`/observe` should return a user-facing error when:

- observe capability is unavailable
- active session ID is empty
- rollout file is missing
- rollout file cannot be opened

Errors should be wrapped with context, for example:

- `codex observe: rollout file not found for session <id>`
- `codex observe: open rollout file: <wrapped error>`

### Runtime failures

During active observation:

- malformed JSON lines should be skipped with `slog.Warn`, not crash observation
- transient file reads should continue polling
- file truncation or replacement should be handled safely by re-seeking from the new end if needed
- explicit rollout failure events should surface as `EventError`

### Replacement behavior

If `/observe` is invoked while another observe session is active:

- stop the old observer first
- then start the new one
- if the new one fails to start, the engine should end with no active observer rather than restoring the old one

This keeps behavior predictable and simple.

## Telegram-Specific Notes

Telegram is the primary target platform for the command UX, but no Telegram-specific observe protocol is required.

The only Telegram-specific behavior in this design is:

- `/observe` command entry points are expected to be used from Telegram
- output should match Telegram's current event rendering style

No change is required in `platform/telegram` unless a small helper is needed for cleaner status text or preview reuse.

## Testing

### `agent/codex`

Add unit tests for:

- locating `rollout-<sessionID>.jsonl`
- starting from EOF and ignoring pre-existing lines
- parsing reasoning / tool use / tool result / assistant text
- detecting `task_complete` and emitting final completion
- skipping malformed lines without aborting
- handling file truncation or replacement safely

### `core`

Add engine tests for:

- `/observe` starts observing the active Codex session
- `/observe` does not start when the rollout tail already indicates `task_complete`
- `/observe` rejects unsupported agents and empty session IDs
- `/observe status` reports the active observer correctly
- `/observe stop` clears observer state
- starting a new observe session replaces the old one
- completion-triggered auto-stop clears state
- changing the active session while observing stops the observer instead of retargeting it
- observe mode uses the event rendering path without mutating interactive session state

### Regression coverage

Add regression coverage for:

- no historical replay on observer startup
- one-bot single-observer enforcement
- no user-prompt forwarding during observe mode

## Open Questions Resolved

- **Completion detection:** use `payload.type == "task_complete"` from rollout JSONL
- **Command model:** `/observe`, `/observe status`, `/observe stop`
- **Concurrency model:** one bot, one active observed Codex session
- **Observed content:** forward agent-side output only; do not forward desktop user prompts
- **Already-complete session:** return an informational message and do not start observing
- **Active session changes:** stop observing; do not automatically switch the observer target

## Implementation Notes

- Keep the first version Codex-only even though the capability is generic
- Prefer focused files over extending `agent/codex/session.go` with observer-specific logic
- Reuse existing event handling code paths rather than duplicating Telegram formatting logic
- Do not add platform-name checks in `core`
