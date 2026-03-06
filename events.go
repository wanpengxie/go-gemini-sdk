package gemini

import (
	"context"
	"encoding/json"
	"strings"
)

type eventType string

const (
	eventTypeUnknown        eventType = "unknown"
	eventTypeMessage        eventType = "message"
	eventTypeMessageChunk   eventType = "message_chunk"
	eventTypeThinking       eventType = "thinking"
	eventTypeToolCall       eventType = "tool_call"
	eventTypeToolCallUpdate eventType = "tool_call_update"
	eventTypeCompleted      eventType = "completed"
	eventTypeError          eventType = "error"
)

type sessionEvent struct {
	Type       eventType       `json:"type"`
	RawType    string          `json:"raw_type,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
	TurnID     string          `json:"turn_id,omitempty"`
	Role       string          `json:"role,omitempty"`
	Text       string          `json:"text,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Done       bool            `json:"done,omitempty"`
	Error      string          `json:"error,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
}

func isThinkingRawType(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "thinking", "thinking_chunk", "agent_thinking", "agent_thinking_chunk", "thought", "thought_chunk", "agent_thought_chunk":
		return true
	default:
		return false
	}
}

// ToolKind represents the normalized tool category.
type ToolKind string

const (
	ToolKindUnknown ToolKind = "unknown"
	ToolKindRead    ToolKind = "read"
	ToolKindEdit    ToolKind = "edit"
	ToolKindBash    ToolKind = "bash"
)

// PermissionOption represents one selectable option in request_permission.
type PermissionOption struct {
	// ACP v1 field.
	OptionID string `json:"optionId,omitempty"`
	// Legacy field.
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Description string `json:"description,omitempty"`
}

// OptionIDValue returns the effective option identifier across ACP versions.
func (o PermissionOption) OptionIDValue() string {
	if id := strings.TrimSpace(o.OptionID); id != "" {
		return id
	}
	return strings.TrimSpace(o.ID)
}

func (o PermissionOption) normalizedID() string {
	return o.OptionIDValue()
}

// ToolCallInfo describes the incoming tool invocation request.
type ToolCallInfo struct {
	SessionID string          `json:"session_id,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	ToolKind  ToolKind        `json:"tool_kind,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
}

// CanUseToolFunc selects which permission option should be applied.
type CanUseToolFunc func(ctx context.Context, call ToolCallInfo, options []PermissionOption) (selectedOptionID string, err error)
