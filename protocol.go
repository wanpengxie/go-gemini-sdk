package gemini

import (
	"encoding/json"
	"strings"
)

const (
	methodInitialize               = "initialize"
	methodSessionNew               = "session/new"
	methodSessionPrompt            = "session/prompt"
	methodSessionUpdate            = "session/update"
	methodSessionInterrupt         = "session/interrupt"
	methodRequestPermission        = "request_permission"
	methodSessionRequestPermission = "session/request_permission"
)

type initializeParams struct {
	ProtocolVersion    int            `json:"protocolVersion,omitempty"`
	ClientInfo         map[string]any `json:"clientInfo,omitempty"`
	ClientCapabilities map[string]any `json:"clientCapabilities,omitempty"`
}

type initializeResult struct {
	// ACP v1 response field.
	ProtocolVersion int `json:"protocolVersion,omitempty"`
	// Legacy field.
	LegacyProtocolVersion string `json:"protocol_version,omitempty"`
}

type sessionNewParams struct {
	Cwd        string           `json:"cwd"`
	MCPServers []map[string]any `json:"mcpServers"`
}

type sessionNewResult struct {
	// ACP v1 field.
	SessionIDV2 string `json:"sessionId,omitempty"`
	// Legacy field.
	SessionID string `json:"session_id,omitempty"`
}

func (r sessionNewResult) EffectiveSessionID() string {
	if id := strings.TrimSpace(r.SessionIDV2); id != "" {
		return id
	}
	return strings.TrimSpace(r.SessionID)
}

type promptContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type sessionPromptParams struct {
	SessionID string               `json:"sessionId,omitempty"`
	Prompt    []promptContentBlock `json:"prompt,omitempty"`
}

type sessionPromptResult struct {
	// ACP v1 field.
	StopReason string `json:"stopReason,omitempty"`

	// Legacy fields.
	Accepted bool   `json:"accepted,omitempty"`
	TurnID   string `json:"turn_id,omitempty"`
}

type sessionInterruptParams struct {
	SessionID string `json:"sessionId,omitempty"`
}

type sessionUpdatePayload struct {
	SessionUpdate string          `json:"sessionUpdate,omitempty"`
	Content       json.RawMessage `json:"content,omitempty"`
	ToolCallID    string          `json:"toolCallId,omitempty"`
	Title         string          `json:"title,omitempty"`
	Status        string          `json:"status,omitempty"`
	Kind          string          `json:"kind,omitempty"`
}

type sessionUpdateParams struct {
	// ACP v1 fields.
	SessionIDV2 string                `json:"sessionId,omitempty"`
	Update      *sessionUpdatePayload `json:"update,omitempty"`

	// Legacy fields.
	SessionID  string          `json:"session_id,omitempty"`
	Type       string          `json:"type,omitempty"`
	TurnID     string          `json:"turn_id,omitempty"`
	Role       string          `json:"role,omitempty"`
	Text       string          `json:"text,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Done       bool            `json:"done,omitempty"`
	Error      string          `json:"error,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
}

type requestPermissionParams struct {
	// ACP v1 fields.
	SessionIDV2 string                     `json:"sessionId,omitempty"`
	ToolCallV2  *requestPermissionToolCall `json:"toolCall,omitempty"`

	// Legacy fields.
	SessionID string                     `json:"session_id,omitempty"`
	ToolName  string                     `json:"tool_name,omitempty"`
	ToolKind  ToolKind                   `json:"tool_kind,omitempty"`
	Reason    string                     `json:"reason,omitempty"`
	Args      json.RawMessage            `json:"args,omitempty"`
	Options   []PermissionOption         `json:"options,omitempty"`
	ToolCall  *requestPermissionToolCall `json:"tool_call,omitempty"`
}

type requestPermissionToolCall struct {
	Name       string          `json:"name,omitempty"`
	Kind       ToolKind        `json:"kind,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	Title      string          `json:"title,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	Status     string          `json:"status,omitempty"`
}

type requestPermissionOutcome struct {
	Outcome  string `json:"outcome,omitempty"`
	OptionID string `json:"optionId,omitempty"`
}

type requestPermissionResult struct {
	Outcome *requestPermissionOutcome `json:"outcome,omitempty"`
}
