package gemini

import "testing"

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

func TestWithSandboxEnabled(t *testing.T) {
	o := applyOptions([]Option{WithSandboxEnabled(true)})
	if o.sandboxEnabled == nil || !*o.sandboxEnabled {
		t.Fatal("sandboxEnabled = nil/false, want true")
	}
}
