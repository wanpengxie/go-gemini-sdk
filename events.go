package gemini

import (
	"context"
	"encoding/json"
)

// EventType is the normalized event category exposed by SDK.
type EventType string

const (
	EventTypeUnknown        EventType = "unknown"
	EventTypeMessage        EventType = "message"
	EventTypeMessageChunk   EventType = "message_chunk"
	EventTypeToolCall       EventType = "tool_call"
	EventTypeToolCallUpdate EventType = "tool_call_update"
	EventTypeCompleted      EventType = "completed"
	EventTypeError          EventType = "error"
)

// SessionEvent is a flattened ACP session/update event.
type SessionEvent struct {
	Type       EventType       `json:"type"`
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
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Description string `json:"description,omitempty"`
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
