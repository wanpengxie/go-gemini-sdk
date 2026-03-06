//go:build integration

package gemini

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const integrationEnv = "GEMINI_SDK_INTEGRATION"

func TestIntegrationQuery(t *testing.T) {
	requireIntegrationEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	out, err := runGoExample(ctx, "./examples/quickstart")
	if err != nil {
		t.Fatalf("quickstart failed: %v\noutput:\n%s", err, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("quickstart output is empty")
	}
}

func TestIntegrationClientSendReceiveMessages(t *testing.T) {
	requireIntegrationEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	out, err := runGoExample(ctx, "./examples/multi_turn")
	if err != nil {
		t.Fatalf("multi_turn failed: %v\noutput:\n%s", err, out)
	}
	if strings.Count(out, ">>> ") < 2 {
		t.Fatalf("multi_turn output seems incomplete:\n%s", out)
	}
}

func TestIntegrationOptionsAffectCLIInvocation(t *testing.T) {
	bin := requireIntegrationEnv(t)
	wrapper := newGeminiWrapper(t, bin)
	workDir := t.TempDir()
	includeA := filepath.Join(workDir, "include-a")
	includeB := filepath.Join(workDir, "include-b")
	if err := os.MkdirAll(includeA, 0o755); err != nil {
		t.Fatalf("mkdir includeA: %v", err)
	}
	if err := os.MkdirAll(includeB, 0o755); err != nil {
		t.Fatalf("mkdir includeB: %v", err)
	}

	policyFile := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(policyFile, []byte("rules = []\n"), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	out, err := Query(ctx, "请只回复 OK", []Option{
		WithBinaryPath(wrapper.scriptPath),
		WithEnv(
			"GO_GEMINI_WRAPPER_LOG="+wrapper.logPrefix,
			"GO_GEMINI_WRAPPER_TARGET="+bin,
			"GO_GEMINI_TEST_MARKER=integration-options",
		),
		WithWorkDir(workDir),
		WithModel("auto"),
		WithApprovalMode(ApprovalModeDefault),
		WithAllowedTools([]string{"read_file"}),
		WithAddDirs(includeA, includeB),
		WithPolicyPaths(policyFile),
	}...)
	if err != nil {
		t.Fatalf("query with options failed: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("query with options returned empty output")
	}

	inv := wrapper.readInvocation(t)
	if !samePath(inv.Pwd, workDir) {
		t.Fatalf("wrapper pwd = %q, want %q", inv.Pwd, workDir)
	}
	if inv.Env["GO_GEMINI_TEST_MARKER"] != "integration-options" {
		t.Fatalf("env GO_GEMINI_TEST_MARKER = %q, want %q", inv.Env["GO_GEMINI_TEST_MARKER"], "integration-options")
	}
	if !hasArgPair(inv.Args, "--model", "auto") {
		t.Fatalf("missing --model auto in args: %v", inv.Args)
	}
	if !hasArgPair(inv.Args, "--approval-mode", ApprovalModeDefault) {
		t.Fatalf("missing --approval-mode %s in args: %v", ApprovalModeDefault, inv.Args)
	}
	if !hasArgPair(inv.Args, "--allowed-tools", "read_file") {
		t.Fatalf("missing --allowed-tools read_file in args: %v", inv.Args)
	}
	if !hasArgPair(inv.Args, "--include-directories", includeA+","+includeB) {
		t.Fatalf("missing --include-directories in args: %v", inv.Args)
	}
	if !hasArgPair(inv.Args, "--policy", policyFile) {
		t.Fatalf("missing --policy %s in args: %v", policyFile, inv.Args)
	}
}

func TestIntegrationExcludedToolsAreConvertedToPolicy(t *testing.T) {
	bin := requireIntegrationEnv(t)
	wrapper := newGeminiWrapper(t, bin)

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	out, err := Query(ctx, "请只回复 OK", []Option{
		WithBinaryPath(wrapper.scriptPath),
		WithEnv(
			"GO_GEMINI_WRAPPER_LOG="+wrapper.logPrefix,
			"GO_GEMINI_WRAPPER_TARGET="+bin,
		),
		WithExcludedTools([]string{"run_shell_command", "write_file"}),
	}...)
	if err != nil {
		t.Fatalf("query with excluded tools failed: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("query with excluded tools returned empty output")
	}

	inv := wrapper.readInvocation(t)
	if containsArg(inv.Args, "--excluded-tools") {
		t.Fatalf("unexpected --excluded-tools flag in args: %v", inv.Args)
	}
	policyPaths := argValues(inv.Args, "--policy")
	if len(policyPaths) != 1 {
		t.Fatalf("policy paths len = %d, want 1; args=%v", len(policyPaths), inv.Args)
	}

	text := inv.PolicyFiles[policyPaths[0]]
	if text == "" {
		t.Fatalf("missing copied policy for %s", policyPaths[0])
	}
	if !strings.Contains(text, `toolName = "run_shell_command"`) {
		t.Fatalf("generated policy missing run_shell_command:\n%s", text)
	}
	if !strings.Contains(text, `toolName = "write_file"`) {
		t.Fatalf("generated policy missing write_file:\n%s", text)
	}
}

func runGoExample(ctx context.Context, target string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "run", target)
	cmd.Dir = "."
	cmd.Env = append(os.Environ(), "GEMINI_BINARY="+os.Getenv("GEMINI_BINARY"))

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func requireIntegrationEnv(t *testing.T) string {
	t.Helper()
	if strings.TrimSpace(os.Getenv(integrationEnv)) != "1" {
		t.Skipf("skip integration test: set %s=1 to enable", integrationEnv)
	}
	bin := strings.TrimSpace(os.Getenv("GEMINI_BINARY"))
	if bin == "" {
		t.Skip("skip integration test: set GEMINI_BINARY to a working gemini executable path")
	}
	if !filepath.IsAbs(bin) {
		t.Skip("skip integration test: GEMINI_BINARY must be an absolute path")
	}
	info, err := os.Stat(bin)
	if err != nil || info.IsDir() {
		t.Skipf("skip integration test: GEMINI_BINARY not available: %s", bin)
	}
	return bin
}

type geminiWrapper struct {
	scriptPath string
	logPrefix  string
}

type wrapperInvocation struct {
	Args        []string
	Env         map[string]string
	Pwd         string
	PolicyFiles map[string]string
}

func newGeminiWrapper(t *testing.T, target string) geminiWrapper {
	t.Helper()

	dir := t.TempDir()
	logPrefix := filepath.Join(dir, "capture")
	scriptPath := filepath.Join(dir, "gemini-wrapper.sh")
	script := `#!/bin/sh
set -eu
log_prefix="${GO_GEMINI_WRAPPER_LOG:?}"
printf '%s\n' "$PWD" > "${log_prefix}.pwd"
printf '%s\n' "$@" > "${log_prefix}.args"
env > "${log_prefix}.env"
policy_index=0
prev=""
for arg in "$@"; do
	if [ "$prev" = "--policy" ]; then
		printf '%s\n' "$arg" >> "${log_prefix}.policy_paths"
		if [ -f "$arg" ]; then
			cp "$arg" "${log_prefix}.policy.${policy_index}"
		fi
		policy_index=$((policy_index + 1))
	fi
	prev="$arg"
done
exec "${GO_GEMINI_WRAPPER_TARGET:?}" "$@"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write wrapper script: %v", err)
	}
	_ = target
	return geminiWrapper{
		scriptPath: scriptPath,
		logPrefix:  logPrefix,
	}
}

func (w geminiWrapper) readInvocation(t *testing.T) wrapperInvocation {
	t.Helper()

	return wrapperInvocation{
		Args:        readLines(t, w.logPrefix+".args"),
		Env:         readEnvLines(t, w.logPrefix+".env"),
		Pwd:         strings.TrimSpace(readFirstLine(t, w.logPrefix+".pwd")),
		PolicyFiles: readPolicyCopies(t, w.logPrefix),
	}
}

func readFirstLine(t *testing.T, path string) string {
	t.Helper()
	lines := readLines(t, path)
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	lines, ok := readLinesWithRetry(path, 20, 100*time.Millisecond)
	if !ok {
		t.Fatalf("read %s: timed out waiting for wrapper output", path)
	}
	return lines
}

func readLinesWithRetry(path string, attempts int, delay time.Duration) ([]string, bool) {
	for i := 0; i < attempts; i++ {
		f, err := os.Open(path)
		if err == nil {
			defer f.Close()
			var lines []string
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				lines = append(lines, scanner.Text())
			}
			if scanner.Err() == nil {
				return lines, true
			}
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	return nil, false
}

func readEnvLines(t *testing.T, path string) map[string]string {
	t.Helper()
	lines := readLines(t, path)
	env := make(map[string]string, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		env[key] = value
	}
	return env
}

func readPolicyCopies(t *testing.T, logPrefix string) map[string]string {
	t.Helper()

	paths, ok := readLinesWithRetry(logPrefix+".policy_paths", 5, 50*time.Millisecond)
	if !ok {
		return nil
	}

	files := make(map[string]string, len(paths))
	for i, original := range paths {
		content, err := os.ReadFile(logPrefix + ".policy." + strconv.Itoa(i))
		if err != nil {
			continue
		}
		files[original] = string(content)
	}
	return files
}

func hasArgPair(args []string, name, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name && args[i+1] == value {
			return true
		}
	}
	return false
}

func argValues(args []string, name string) []string {
	values := make([]string, 0, 2)
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			values = append(values, args[i+1])
		}
	}
	return values
}

func containsArg(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}

func samePath(a, b string) bool {
	return resolvePath(a) == resolvePath(b)
}

func resolvePath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil && resolved != "" {
		return resolved
	}
	return filepath.Clean(path)
}
