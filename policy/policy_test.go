package policy

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	gemini "github.com/wanpengxie/go-gemini-sdk"
)

func TestNewHandlerFromJSONSupportsPermissionsWrapper(t *testing.T) {
	baseDir := t.TempDir()
	handler, err := NewHandlerFromJSONWithBaseDir(`{
		"permissions": {
			"deny": ["Bash(rm -rf *)"]
		}
	}`, baseDir)
	if err != nil {
		t.Fatalf("NewHandlerFromJSONWithBaseDir() error = %v", err)
	}

	got := callPolicyHandler(t, handler, gemini.ToolCallInfo{
		ToolName: "bash",
		ToolKind: gemini.ToolKindBash,
		Args:     mustRaw(t, map[string]any{"command": "rm -rf /tmp/demo"}),
	})
	if got != "reject_once" {
		t.Fatalf("selected = %q, want %q", got, "reject_once")
	}
}

func TestPolicyCommandChainingBypassBlocked(t *testing.T) {
	baseDir := t.TempDir()
	handler, err := NewHandlerFromJSONWithBaseDir(`{
		"deny": ["Bash(rm -rf *)"]
	}`, baseDir)
	if err != nil {
		t.Fatalf("NewHandlerFromJSONWithBaseDir() error = %v", err)
	}

	got := callPolicyHandler(t, handler, gemini.ToolCallInfo{
		ToolName: "bash",
		ToolKind: gemini.ToolKindBash,
		Args:     mustRaw(t, map[string]any{"command": "echo start; rm -rf /"}),
	})
	if got != "reject_once" {
		t.Fatalf("selected = %q, want %q", got, "reject_once")
	}
}

func TestPolicyPathVariantsAreNormalized(t *testing.T) {
	baseDir := t.TempDir()
	handler, err := NewHandlerFromJSONWithBaseDir(`{
		"deny": ["Read(./.env)"]
	}`, baseDir)
	if err != nil {
		t.Fatalf("NewHandlerFromJSONWithBaseDir() error = %v", err)
	}

	cases := []string{
		".env",
		"./src/../.env",
		filepath.Join(baseDir, ".env"),
	}
	for _, path := range cases {
		got := callPolicyHandler(t, handler, gemini.ToolCallInfo{
			ToolName: "read",
			ToolKind: gemini.ToolKindRead,
			Args:     mustRaw(t, map[string]any{"path": path}),
		})
		if got != "reject_once" {
			t.Fatalf("path=%q selected = %q, want %q", path, got, "reject_once")
		}
	}
}

func TestPolicyGlobDoesNotCrossDirectories(t *testing.T) {
	baseDir := t.TempDir()
	handler, err := NewHandlerFromJSONWithBaseDir(`{
		"allow": ["Read(./*.go)"]
	}`, baseDir)
	if err != nil {
		t.Fatalf("NewHandlerFromJSONWithBaseDir() error = %v", err)
	}

	got := callPolicyHandler(t, handler, gemini.ToolCallInfo{
		ToolName: "read",
		ToolKind: gemini.ToolKindRead,
		Args:     mustRaw(t, map[string]any{"path": "./main.go"}),
	})
	if got != "allow_once" {
		t.Fatalf("top-level selected = %q, want %q", got, "allow_once")
	}

	got = callPolicyHandler(t, handler, gemini.ToolCallInfo{
		ToolName: "read",
		ToolKind: gemini.ToolKindRead,
		Args:     mustRaw(t, map[string]any{"path": "./secret/private.go"}),
	})
	if got != "" {
		t.Fatalf("nested selected = %q, want empty", got)
	}
}

func TestPolicyAllowNeedsAllAtomicCommands(t *testing.T) {
	baseDir := t.TempDir()
	handler, err := NewHandlerFromJSONWithBaseDir(`{
		"allow": ["Bash(echo *)", "Bash(ls *)"]
	}`, baseDir)
	if err != nil {
		t.Fatalf("NewHandlerFromJSONWithBaseDir() error = %v", err)
	}

	got := callPolicyHandler(t, handler, gemini.ToolCallInfo{
		ToolName: "bash",
		ToolKind: gemini.ToolKindBash,
		Args:     mustRaw(t, map[string]any{"command": "echo ok; ls -la"}),
	})
	if got != "allow_once" {
		t.Fatalf("allowed selected = %q, want %q", got, "allow_once")
	}

	got = callPolicyHandler(t, handler, gemini.ToolCallInfo{
		ToolName: "bash",
		ToolKind: gemini.ToolKindBash,
		Args:     mustRaw(t, map[string]any{"command": "echo ok; rm -rf /"}),
	})
	if got != "" {
		t.Fatalf("mixed selected = %q, want empty", got)
	}
}

func TestPolicyShellParseFailureFailClosed(t *testing.T) {
	baseDir := t.TempDir()
	handler, err := NewHandlerFromJSONWithBaseDir(`{
		"deny": ["Bash(rm -rf *)"]
	}`, baseDir)
	if err != nil {
		t.Fatalf("NewHandlerFromJSONWithBaseDir() error = %v", err)
	}

	got := callPolicyHandler(t, handler, gemini.ToolCallInfo{
		ToolName: "bash",
		ToolKind: gemini.ToolKindBash,
		Args:     mustRaw(t, map[string]any{"command": "echo $("}),
	})
	if got != "reject_once" {
		t.Fatalf("selected = %q, want %q", got, "reject_once")
	}
}

func TestNewHandlerFromJSONNoMatchReturnsEmpty(t *testing.T) {
	baseDir := t.TempDir()
	handler, err := NewHandlerFromJSONWithBaseDir(`{"deny":["Bash(rm -rf *)"]}`, baseDir)
	if err != nil {
		t.Fatalf("NewHandlerFromJSONWithBaseDir() error = %v", err)
	}

	got := callPolicyHandler(t, handler, gemini.ToolCallInfo{
		ToolName: "read",
		ToolKind: gemini.ToolKindRead,
		Args:     mustRaw(t, map[string]any{"path": "README.md"}),
	})
	if got != "" {
		t.Fatalf("selected = %q, want empty", got)
	}
}

func TestNewHandlerFromJSONInvalidConfig(t *testing.T) {
	if _, err := NewHandlerFromJSONWithBaseDir("{", t.TempDir()); err == nil {
		t.Fatal("NewHandlerFromJSONWithBaseDir() error = nil, want non-nil")
	}
}

func callPolicyHandler(t *testing.T, handler gemini.CanUseToolFunc, call gemini.ToolCallInfo) string {
	t.Helper()

	options := []gemini.PermissionOption{
		{ID: "allow_once"},
		{ID: "ask_once"},
		{ID: "reject_once"},
	}
	got, err := handler(context.Background(), call, options)
	if err != nil {
		t.Fatalf("handler error = %v", err)
	}
	return got
}

func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}
	return b
}
