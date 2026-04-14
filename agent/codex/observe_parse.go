package codex

import (
	"encoding/json"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

type rolloutEnvelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type parsedRolloutEvent struct {
	Event       *core.Event
	Done        bool
	CallID      string
	ToolName    string
	RawToolName string
	SourceType  string
}

func parseRolloutLine(line []byte, sessionID string) (*parsedRolloutEvent, error) {
	var env rolloutEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, err
	}

	switch env.Type {
	case "event_msg":
		return parseRolloutEventMessage(env.Payload, sessionID)
	case "response_item":
		return parseRolloutResponseItem(env.Payload, sessionID)
	default:
		return nil, nil
	}
}

func parseRolloutEventMessage(raw json.RawMessage, sessionID string) (*parsedRolloutEvent, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}

	switch mapString(payload, "type") {
	case "agent_reasoning":
		text := strings.TrimSpace(mapString(payload, "text"))
		if text == "" {
			return nil, nil
		}
		return &parsedRolloutEvent{
			Event: &core.Event{Type: core.EventThinking, Content: text},
		}, nil
	case "agent_message":
		msg := strings.TrimSpace(mapString(payload, "message"))
		if msg == "" {
			return nil, nil
		}
		if strings.EqualFold(mapString(payload, "phase"), "commentary") {
			return &parsedRolloutEvent{
				Event: &core.Event{Type: core.EventThinking, Content: msg},
			}, nil
		}
		return &parsedRolloutEvent{
			Event: &core.Event{Type: core.EventText, Content: msg, SessionID: sessionID},
		}, nil
	case "exec_command_end":
		output := truncate(strings.TrimSpace(firstNonEmptyMapString(payload, "aggregated_output", "formatted_output", "message")), 500)
		exitCode, hasExitCode := firstMapInt(payload, "exit_code", "exitCode")
		var exitCodePtr *int
		if hasExitCode {
			exitCodePtr = intPtr(exitCode)
		}
		status := strings.TrimSpace(firstNonEmptyMapString(payload, "status"))
		success := codexToolSuccess(status, exitCodePtr)
		return &parsedRolloutEvent{
			Event: &core.Event{
				Type:         core.EventToolResult,
				ToolName:     "Bash",
				ToolResult:   output,
				ToolStatus:   status,
				ToolExitCode: exitCodePtr,
				ToolSuccess:  &success,
			},
			CallID:      mapString(payload, "call_id"),
			ToolName:    "Bash",
			RawToolName: "exec_command",
			SourceType:  "exec_command_end",
		}, nil
	case "task_complete":
		return &parsedRolloutEvent{
			Event: &core.Event{
				Type:      core.EventResult,
				Content:   strings.TrimSpace(mapString(payload, "last_agent_message")),
				SessionID: sessionID,
				Done:      true,
			},
			Done: true,
		}, nil
	default:
		return nil, nil
	}
}

func parseRolloutResponseItem(raw json.RawMessage, sessionID string) (*parsedRolloutEvent, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}

	return parseRolloutResponseItemPayload(payload, sessionID)
}

func parseRolloutResponseItemPayload(payload map[string]any, sessionID string) (*parsedRolloutEvent, error) {
	payloadType := mapString(payload, "type")
	callID := mapString(payload, "call_id")

	switch payloadType {
	case "reasoning":
		text := strings.TrimSpace(extractItemText(payload, "summary", "summary_text"))
		if text == "" {
			return nil, nil
		}
		return &parsedRolloutEvent{
			Event: &core.Event{Type: core.EventThinking, Content: text},
		}, nil
	case "message":
		if !strings.EqualFold(strings.TrimSpace(mapString(payload, "role")), "assistant") {
			return nil, nil
		}
		text := strings.TrimSpace(extractItemText(payload, "content", "output_text"))
		if text == "" {
			return nil, nil
		}
		if strings.EqualFold(mapString(payload, "phase"), "commentary") {
			return &parsedRolloutEvent{
				Event: &core.Event{Type: core.EventThinking, Content: text},
			}, nil
		}
		return &parsedRolloutEvent{
			Event: &core.Event{Type: core.EventText, Content: text, SessionID: sessionID},
		}, nil
	case "function_call":
		rawToolName := strings.TrimSpace(mapString(payload, "name"))
		toolName, toolInput := rolloutFunctionCallDisplay(rawToolName, payload["arguments"], payload["input"])
		return &parsedRolloutEvent{
			Event: &core.Event{
				Type:      core.EventToolUse,
				ToolName:  toolName,
				ToolInput: toolInput,
			},
			CallID:      callID,
			ToolName:    toolName,
			RawToolName: rawToolName,
			SourceType:  payloadType,
		}, nil
	case "web_search_call":
		return &parsedRolloutEvent{
			Event: &core.Event{
				Type:      core.EventToolUse,
				ToolName:  "WebSearch",
				ToolInput: codexExtractToolInput(payload),
			},
		}, nil
	default:
		if toolName, ok := rolloutKnownToolCallDisplay(payloadType); ok {
			return &parsedRolloutEvent{
				Event: &core.Event{
					Type:      core.EventToolUse,
					ToolName:  toolName,
					ToolInput: codexExtractToolInput(payload),
				},
			}, nil
		}
		return nil, nil
	}
}

func rolloutKnownToolCallDisplay(payloadType string) (string, bool) {
	baseType, ok := strings.CutSuffix(strings.TrimSpace(payloadType), "_call")
	if !ok {
		return "", false
	}
	toolName, known := codexToolNames[baseType]
	if !known || toolName == "WebSearch" {
		return "", false
	}
	return toolName, true
}

func rolloutFunctionCallDisplay(rawToolName string, arguments, input any) (string, string) {
	switch rawToolName {
	case "exec_command":
		if cmd := rolloutJSONFieldString(arguments, "cmd"); cmd != "" {
			return "Bash", cmd
		}
		if cmd := rolloutJSONFieldString(input, "cmd"); cmd != "" {
			return "Bash", cmd
		}
		return "Bash", stringifyAny(arguments)
	default:
		toolInput := stringifyAny(arguments)
		if toolInput == "" {
			toolInput = stringifyAny(input)
		}
		return rawToolName, toolInput
	}
}

func rolloutJSONFieldString(raw any, key string) string {
	switch x := raw.(type) {
	case map[string]any:
		return strings.TrimSpace(mapString(x, key))
	case string:
		text := strings.TrimSpace(x)
		if text == "" || !looksLikeStructuredJSON(text) {
			return ""
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			return ""
		}
		return strings.TrimSpace(mapString(parsed, key))
	default:
		return ""
	}
}

func mapString(raw map[string]any, key string) string {
	v, ok := raw[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func stringifyAny(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
}

func looksLikeStructuredJSON(s string) bool {
	if len(s) < 2 {
		return false
	}
	return (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
		(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]"))
}

func firstNonEmptyMapString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if s := strings.TrimSpace(mapString(raw, key)); s != "" {
			return s
		}
	}
	return ""
}

func firstMapInt(raw map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		v, ok := raw[key]
		if !ok {
			continue
		}
		if n, ok := toInt(v); ok {
			return n, true
		}
	}
	return 0, false
}

func intPtr(v int) *int {
	return &v
}
