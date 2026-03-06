package gemini

import "encoding/json"

// Message is a typed turn-scoped message exposed by the high-level SDK API.
type Message interface {
	messageType() string
}

// ContentBlock is a typed content fragment within an AssistantMessage.
type ContentBlock interface {
	contentBlockType() string
}

// AssistantMessage represents one assistant-side semantic update.
type AssistantMessage struct {
	SessionID string         `json:"session_id,omitempty"`
	TurnID    string         `json:"turn_id,omitempty"`
	Content   []ContentBlock `json:"content"`
}

func (m *AssistantMessage) messageType() string { return "assistant" }

// ResultMessage marks the terminal state of one turn.
type ResultMessage struct {
	SessionID  string `json:"session_id,omitempty"`
	TurnID     string `json:"turn_id,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (m *ResultMessage) messageType() string { return "result" }

// TextBlock represents assistant text output.
type TextBlock struct {
	Text string `json:"text"`
}

func (b *TextBlock) contentBlockType() string { return "text" }

// ThinkingBlock represents assistant thinking output.
type ThinkingBlock struct {
	Thinking string `json:"thinking"`
}

func (b *ThinkingBlock) contentBlockType() string { return "thinking" }

// ToolUseBlock represents a tool invocation request from the model.
type ToolUseBlock struct {
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

func (b *ToolUseBlock) contentBlockType() string { return "tool_use" }

// ToolResultBlock represents a tool execution result.
type ToolResultBlock struct {
	ToolUseID string `json:"tool_use_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Content   any    `json:"content,omitempty"`
}

func (b *ToolResultBlock) contentBlockType() string { return "tool_result" }

func messageFromEvent(ev sessionEvent) Message {
	block := contentBlockFromEvent(ev)
	if block == nil {
		return nil
	}
	return &AssistantMessage{
		SessionID: ev.SessionID,
		TurnID:    ev.TurnID,
		Content:   []ContentBlock{block},
	}
}

func contentBlockFromEvent(ev sessionEvent) ContentBlock {
	switch ev.Type {
	case eventTypeMessage, eventTypeMessageChunk:
		if ev.Text == "" {
			return nil
		}
		return &TextBlock{Text: ev.Text}
	case eventTypeThinking:
		if ev.Text == "" {
			return nil
		}
		return &ThinkingBlock{Thinking: ev.Text}
	case eventTypeToolCall:
		return &ToolUseBlock{
			ID:    ev.ToolCallID,
			Name:  ev.ToolName,
			Input: decodeJSONObject(ev.Data),
		}
	case eventTypeToolCallUpdate:
		return &ToolResultBlock{
			ToolUseID: ev.ToolCallID,
			Name:      ev.ToolName,
			Content:   decodeJSONValue(ev.Data),
		}
	default:
		return nil
	}
}

func decodeJSONObject(raw json.RawMessage) map[string]any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func decodeJSONValue(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return string(raw)
	}
	return out
}
