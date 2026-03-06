package gemini

import (
	"context"
	"testing"
)

func TestWithPolicyPathsSkipsEmpty(t *testing.T) {
	o := applyOptions([]Option{
		WithPolicyPaths(" /a.toml ", "", "   ", "/b"),
	})

	if len(o.policyPaths) != 2 {
		t.Fatalf("policyPaths len = %d, want 2", len(o.policyPaths))
	}
	if o.policyPaths[0] != "/a.toml" {
		t.Fatalf("policyPaths[0] = %q, want %q", o.policyPaths[0], "/a.toml")
	}
	if o.policyPaths[1] != "/b" {
		t.Fatalf("policyPaths[1] = %q, want %q", o.policyPaths[1], "/b")
	}
}

func TestWithApprovalCallbackAlias(t *testing.T) {
	called := false
	cb := func(ctx context.Context, call ToolCallInfo, options []PermissionOption) (string, error) {
		_ = ctx
		_ = call
		_ = options
		called = true
		return "", nil
	}

	o := applyOptions([]Option{WithApprovalCallback(cb)})
	if o.canUseTool == nil {
		t.Fatal("canUseTool is nil, want non-nil")
	}

	_, _ = o.canUseTool(context.Background(), ToolCallInfo{}, nil)
	if !called {
		t.Fatal("approval callback not invoked")
	}
}

func TestWithSandboxEnabled(t *testing.T) {
	o := applyOptions([]Option{WithSandboxEnabled(true)})
	if o.sandboxEnabled == nil || !*o.sandboxEnabled {
		t.Fatal("sandboxEnabled = nil/false, want true")
	}
}
