package policy

import (
	"context"
	"encoding/json"
	"testing"

	gemini "github.com/wanpengxie/go-gemini-sdk"
)

func TestNewHandlerFromJSONRejectAskAllow(t *testing.T) {
	handler, err := NewHandlerFromJSON(`{
		"allow": ["Read", "Bash(ls *)"],
		"deny":  ["Bash(rm -rf *)"],
		"ask":   ["Bash(git push *)"]
	}`)
	if err != nil {
		t.Fatalf("NewHandlerFromJSON() error = %v", err)
	}

	options := []gemini.PermissionOption{
		{ID: "allow_once"},
		{ID: "ask_once"},
		{ID: "reject_once"},
	}

	got, err := handler(context.Background(), gemini.ToolCallInfo{
		ToolName: "bash",
		ToolKind: gemini.ToolKindBash,
		Args:     mustRaw(t, map[string]any{"command": "rm -rf /tmp/demo"}),
	}, options)
	if err != nil {
		t.Fatalf("handler deny call error = %v", err)
	}
	if got != "reject_once" {
		t.Fatalf("deny selected = %q, want %q", got, "reject_once")
	}

	got, err = handler(context.Background(), gemini.ToolCallInfo{
		ToolName: "bash",
		ToolKind: gemini.ToolKindBash,
		Args:     mustRaw(t, map[string]any{"command": "git push origin main"}),
	}, options)
	if err != nil {
		t.Fatalf("handler ask call error = %v", err)
	}
	if got != "ask_once" {
		t.Fatalf("ask selected = %q, want %q", got, "ask_once")
	}

	got, err = handler(context.Background(), gemini.ToolCallInfo{
		ToolName: "read",
		ToolKind: gemini.ToolKindRead,
		Args:     mustRaw(t, map[string]any{"path": "README.md"}),
	}, options)
	if err != nil {
		t.Fatalf("handler allow call error = %v", err)
	}
	if got != "allow_once" {
		t.Fatalf("allow selected = %q, want %q", got, "allow_once")
	}
}

func TestNewHandlerFromJSONNoMatchReturnsEmpty(t *testing.T) {
	handler, err := NewHandlerFromJSON(`{"deny":["Bash(rm -rf *)"]}`)
	if err != nil {
		t.Fatalf("NewHandlerFromJSON() error = %v", err)
	}

	got, err := handler(context.Background(), gemini.ToolCallInfo{
		ToolName: "read",
		ToolKind: gemini.ToolKindRead,
		Args:     mustRaw(t, map[string]any{"path": "README.md"}),
	}, []gemini.PermissionOption{{ID: "allow_once"}})
	if err != nil {
		t.Fatalf("handler error = %v", err)
	}
	if got != "" {
		t.Fatalf("selected = %q, want empty", got)
	}
}

func TestNewHandlerFromJSONInvalidConfig(t *testing.T) {
	if _, err := NewHandlerFromJSON("{"); err == nil {
		t.Fatal("NewHandlerFromJSON() error = nil, want non-nil")
	}
}

func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}
	return b
}
