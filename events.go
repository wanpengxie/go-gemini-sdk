package gemini

import (
	"context"
	"encoding/json"
	"strings"
)

// EventType is the normalized event category exposed by SDK.
type EventType string

const (
	EventTypeUnknown        EventType = "unknown"
	EventTypeMessage        EventType = "message"
	EventTypeMessageChunk   EventType = "message_chunk"
	EventTypeThinking       EventType = "thinking"
	EventTypeToolCall       EventType = "tool_call"
	EventTypeToolCallUpdate EventType = "tool_call_update"
	EventTypeCompleted      EventType = "completed"
	EventTypeError          EventType = "error"
)

// SessionEvent is a flattened ACP session/update event.
type SessionEvent struct {
	Type       EventType       `json:"type"`
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

// BlockKind is the normalized stream block category.
type BlockKind string

const (
	BlockKindUnknown    BlockKind = "unknown"
	BlockKindText       BlockKind = "text"
	BlockKindThinking   BlockKind = "thinking"
	BlockKindToolCall   BlockKind = "tool_call"
	BlockKindToolResult BlockKind = "tool_result"
	BlockKindDone       BlockKind = "done"
	BlockKindError      BlockKind = "error"
)

// StreamBlock is a higher-level structured view converted from SessionEvent.
type StreamBlock struct {
	Kind       BlockKind       `json:"kind"`
	SessionID  string          `json:"session_id,omitempty"`
	TurnID     string          `json:"turn_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Text       string          `json:"text,omitempty"`
	Error      string          `json:"error,omitempty"`
	Done       bool            `json:"done,omitempty"`
	RawType    string          `json:"raw_type,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
}

// ToBlock converts a low-level SessionEvent into a structured StreamBlock.
func (e SessionEvent) ToBlock() StreamBlock {
	block := StreamBlock{
		Kind:       BlockKindUnknown,
		SessionID:  e.SessionID,
		TurnID:     e.TurnID,
		ToolName:   e.ToolName,
		ToolCallID: e.ToolCallID,
		Text:       e.Text,
		Error:      e.Error,
		Done:       e.Done,
		RawType:    e.RawType,
		Data:       e.Data,
	}

	switch {
	case e.Error != "" || e.Type == EventTypeError:
		block.Kind = BlockKindError
	case e.Done || e.Type == EventTypeCompleted:
		block.Kind = BlockKindDone
	case e.Type == EventTypeToolCall:
		block.Kind = BlockKindToolCall
	case e.Type == EventTypeToolCallUpdate:
		block.Kind = BlockKindToolResult
	case e.Type == EventTypeThinking || isThinkingRawType(e.RawType):
		block.Kind = BlockKindThinking
	case e.Type == EventTypeMessage || e.Type == EventTypeMessageChunk:
		block.Kind = BlockKindText
	default:
		block.Kind = BlockKindUnknown
	}
	return block
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
