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

// PermissionRequest describes an incoming request_permission call.
type PermissionRequest struct {
	SessionID string          `json:"session_id,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
}

// PermissionResult is the callback result for request_permission.
type PermissionResult struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
}

// CanUseToolFunc decides whether a tool invocation should be allowed.
type CanUseToolFunc func(ctx context.Context, req PermissionRequest) (PermissionResult, error)
