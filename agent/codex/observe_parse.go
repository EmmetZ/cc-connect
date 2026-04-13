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
	Event    *core.Event
	Done     bool
	CallID   string
	ToolName string
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
		return parseRolloutResponseItem(env.Payload)
	default:
		return nil, nil
	}
}

func parseRolloutEventMessage(raw json.RawMessage, sessionID string) (*parsedRolloutEvent, error) {
	var payload struct {
		Type             string `json:"type"`
		Text             string `json:"text"`
		Message          string `json:"message"`
		LastAgentMessage string `json:"last_agent_message"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}

	switch payload.Type {
	case "agent_reasoning":
		text := strings.TrimSpace(payload.Text)
		if text == "" {
			return nil, nil
		}
		return &parsedRolloutEvent{
			Event: &core.Event{Type: core.EventThinking, Content: text},
		}, nil
	case "agent_message":
		msg := strings.TrimSpace(payload.Message)
		if msg == "" {
			return nil, nil
		}
		return &parsedRolloutEvent{
			Event: &core.Event{Type: core.EventText, Content: msg, SessionID: sessionID},
		}, nil
	case "task_complete":
		return &parsedRolloutEvent{
			Event: &core.Event{
				Type:      core.EventResult,
				Content:   strings.TrimSpace(payload.LastAgentMessage),
				SessionID: sessionID,
				Done:      true,
			},
			Done: true,
		}, nil
	default:
		return nil, nil
	}
}

func parseRolloutResponseItem(raw json.RawMessage) (*parsedRolloutEvent, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}

	payloadType := mapString(payload, "type")
	callID := mapString(payload, "call_id")

	switch payloadType {
	case "function_call", "custom_tool_call":
		toolName := strings.TrimSpace(mapString(payload, "name"))
		toolInput := stringifyAny(payload["arguments"])
		if toolInput == "" {
			toolInput = stringifyAny(payload["input"])
		}
		return &parsedRolloutEvent{
			Event: &core.Event{
				Type:      core.EventToolUse,
				ToolName:  toolName,
				ToolInput: toolInput,
			},
			CallID:   callID,
			ToolName: toolName,
		}, nil
	case "function_call_output", "custom_tool_call_output":
		toolName := strings.TrimSpace(mapString(payload, "name"))
		result := stringifyAny(payload["output"])
		return &parsedRolloutEvent{
			Event: &core.Event{
				Type:       core.EventToolResult,
				ToolName:   toolName,
				ToolResult: result,
			},
			CallID:   callID,
			ToolName: toolName,
		}, nil
	default:
		return nil, nil
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
