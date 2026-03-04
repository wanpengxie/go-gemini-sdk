package gemini

import "encoding/json"

const (
	methodInitialize        = "initialize"
	methodSessionNew        = "session/new"
	methodSessionPrompt     = "session/prompt"
	methodSessionUpdate     = "session/update"
	methodSessionInterrupt  = "session/interrupt"
	methodRequestPermission = "request_permission"
)

type initializeParams struct {
	ProtocolVersion string                 `json:"protocol_version,omitempty"`
	ClientInfo      map[string]any         `json:"client_info,omitempty"`
	Capabilities    map[string]any         `json:"capabilities,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
}

type initializeResult struct {
	ProtocolVersion string `json:"protocol_version,omitempty"`
}

type sessionNewParams struct {
	Model   string `json:"model,omitempty"`
	WorkDir string `json:"work_dir,omitempty"`
}

type sessionNewResult struct {
	SessionID string `json:"session_id"`
}

type sessionPromptParams struct {
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
}

type sessionPromptResult struct {
	Accepted bool   `json:"accepted,omitempty"`
	TurnID   string `json:"turn_id,omitempty"`
}

type sessionInterruptParams struct {
	SessionID string `json:"session_id"`
}

type sessionUpdateParams struct {
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
	SessionID string          `json:"session_id,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
}

type requestPermissionResult struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
}
