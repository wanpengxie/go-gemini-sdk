package gemini

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildCLIArgsIncludesModelAndSandbox(t *testing.T) {
	o := defaultOptions()
	o.model = "gemini-2.5-pro"
	o.sandbox = "workspace-write"

	args := buildCLIArgs(o)
	want := []string{"--experimental-acp", "--model", "gemini-2.5-pro", "--sandbox", "workspace-write"}
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
	o.excludedTools = []string{"edit"}
	o.addDirs = []string{"/repo", "/tmp"}

	args := buildCLIArgs(o)
	want := []string{
		"--experimental-acp",
		"--approval-mode", "auto",
		"--allowed-tools", "bash,read",
		"--excluded-tools", "edit",
		"--include-directories", "/repo,/tmp",
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
