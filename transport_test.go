package gemini

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildCLIArgsIncludesModelAndSandbox(t *testing.T) {
	o := defaultOptions()
	o.model = "gemini-2.5-pro"
	o.sandbox = "workspace-write"

	args := buildCLIArgs(o)
	want := []string{"--experimental-acp", "--model", "gemini-2.5-pro", "--sandbox"}
	if len(args) != len(want) {
		t.Fatalf("args len = %d, want %d", len(args), len(want))
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestBuildCLIArgsIncludesPolicyFlags(t *testing.T) {
	o := defaultOptions()
	o.approvalMode = "auto"
	o.allowedTools = []string{"bash", "read"}
	o.addDirs = []string{"/repo", "/tmp"}
	o.policyPaths = []string{"/etc/gemini-cli/policies", "/workspace/.gemini/policies"}

	args := buildCLIArgs(o)
	want := []string{
		"--experimental-acp",
		"--approval-mode", "auto",
		"--allowed-tools", "bash,read",
		"--include-directories", "/repo,/tmp",
		"--policy", "/etc/gemini-cli/policies",
		"--policy", "/workspace/.gemini/policies",
	}
	if len(args) != len(want) {
		t.Fatalf("args len = %d, want %d (%v)", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestPrepareLaunchOptionsConvertsExcludedToolsToPolicy(t *testing.T) {
	o := defaultOptions()
	o.excludedTools = []string{"run_shell_command", " write_file ", "run_shell_command", ""}
	o.policyPaths = []string{"/tmp/original-policy.toml"}

	prepared, cleanup, err := prepareLaunchOptions(o)
	if err != nil {
		t.Fatalf("prepareLaunchOptions() error = %v", err)
	}
	defer cleanup()

	if len(prepared.excludedTools) != 0 {
		t.Fatalf("prepared.excludedTools = %v, want empty", prepared.excludedTools)
	}
	if len(prepared.policyPaths) != 2 {
		t.Fatalf("prepared.policyPaths len = %d, want 2", len(prepared.policyPaths))
	}
	if prepared.policyPaths[0] != "/tmp/original-policy.toml" {
		t.Fatalf("prepared.policyPaths[0] = %q, want original policy path", prepared.policyPaths[0])
	}

	content, err := os.ReadFile(prepared.policyPaths[1])
	if err != nil {
		t.Fatalf("read generated policy: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, `toolName = "run_shell_command"`) {
		t.Fatalf("generated policy missing run_shell_command rule:\n%s", text)
	}
	if !strings.Contains(text, `toolName = "write_file"`) {
		t.Fatalf("generated policy missing write_file rule:\n%s", text)
	}
	if strings.Contains(text, `toolName = ""`) {
		t.Fatalf("generated policy unexpectedly contains empty tool rule:\n%s", text)
	}
}

func TestBuildCLIArgsSupportsSandboxSwitch(t *testing.T) {
	o := defaultOptions()
	enabled := true
	o.sandboxEnabled = &enabled

	args := buildCLIArgs(o)
	want := []string{"--experimental-acp", "--sandbox"}
	if len(args) != len(want) {
		t.Fatalf("args len = %d, want %d (%v)", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestStderrRingKeepsTail(t *testing.T) {
	ring := newStderrRing(8)
	ring.Append([]byte("12345"))
	ring.Append([]byte("6789"))
	if got, want := ring.String(), "23456789"; got != want {
		t.Fatalf("ring tail = %q, want %q", got, want)
	}
}

func TestFindGeminiConfiguredPath(t *testing.T) {
	got, err := findGemini(context.Background(), "/tmp/custom-gemini")
	if err != nil {
		t.Fatalf("findGemini() error = %v", err)
	}
	if got != "/tmp/custom-gemini" {
		t.Fatalf("findGemini() = %q, want %q", got, "/tmp/custom-gemini")
	}
}

func TestFindGeminiFallsBackToNPMBin(t *testing.T) {
	origLookPath := lookPathFn
	origNPM := npmGlobalBinFn
	origHome := userHomeDirFn
	origCommon := commonPathsFn
	origExec := executableCheck
	t.Cleanup(func() {
		lookPathFn = origLookPath
		npmGlobalBinFn = origNPM
		userHomeDirFn = origHome
		commonPathsFn = origCommon
		executableCheck = origExec
	})

	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	candidate := filepath.Join(binDir, "gemini")
	if err := os.WriteFile(candidate, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write candidate: %v", err)
	}

	lookPathFn = func(file string) (string, error) {
		return "", errors.New("not found")
	}
	npmGlobalBinFn = func(context.Context) (string, error) {
		return binDir, nil
	}
	userHomeDirFn = func() (string, error) {
		return tmp, nil
	}
	commonPathsFn = func(string) []string {
		return nil
	}
	executableCheck = isExecutableFile

	got, err := findGemini(context.Background(), "")
	if err != nil {
		t.Fatalf("findGemini() error = %v", err)
	}
	if got != candidate {
		t.Fatalf("findGemini() = %q, want %q", got, candidate)
	}
}

func TestRealRunnerStartDoesNotTieProcessLifetimeToContext(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}

	runner := &realRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	handle, err := runner.Start(ctx, sh, []string{"-c", "sleep 60"}, nil, "")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- handle.Wait()
	}()

	cancel()

	select {
	case err := <-waitCh:
		t.Fatalf("Wait() returned after startup context cancel: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	if handle.KillGroup != nil {
		_ = handle.KillGroup()
	} else if handle.Kill != nil {
		_ = handle.Kill()
	}

	select {
	case <-waitCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait() did not return after kill")
	}
}
