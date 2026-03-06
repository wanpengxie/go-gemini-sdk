package gemini

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type processRunner interface {
	Start(ctx context.Context, binary string, args []string, env []string, cwd string) (*processHandle, error)
}

type processHandle struct {
	PID       int
	Stdin     io.WriteCloser
	Stdout    io.ReadCloser
	Stderr    io.ReadCloser
	Wait      func() error
	Kill      func() error
	KillGroup func() error
}

type realRunner struct{}

func (r *realRunner) Start(ctx context.Context, binary string, args []string, env []string, cwd string) (*processHandle, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.SysProcAttr = newSysProcAttr()
	if len(env) > 0 {
		cmd.Env = env
	}
	if cwd != "" {
		cmd.Dir = cwd
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, &ProcessError{Op: "stdin_pipe", Err: err}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, &ProcessError{Op: "stdout_pipe", Err: err}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, &ProcessError{Op: "stderr_pipe", Err: err}
	}

	if err := cmd.Start(); err != nil {
		return nil, &ProcessError{Op: "start", Err: err}
	}

	handle := &processHandle{
		PID:    cmd.Process.Pid,
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Wait:   cmd.Wait,
		Kill:   cmd.Process.Kill,
	}
	handle.KillGroup = func() error {
		return killProcessGroup(handle.PID)
	}
	return handle, nil
}

var (
	lookPathFn      = exec.LookPath
	npmGlobalBinFn  = defaultNPMGlobalBin
	userHomeDirFn   = os.UserHomeDir
	commonPathsFn   = defaultCommonGeminiPaths
	executableCheck = isExecutableFile
)

func findGemini(ctx context.Context, configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}

	attempts := make([]string, 0, 8)

	if p, err := lookPathFn("gemini"); err == nil {
		return p, nil
	}
	attempts = append(attempts, "PATH:gemini")

	if npmBin, err := npmGlobalBinFn(ctx); err == nil && npmBin != "" {
		candidate := filepath.Join(npmBin, "gemini")
		attempts = append(attempts, candidate)
		if executableCheck(candidate) {
			return candidate, nil
		}
	}

	if home, err := userHomeDirFn(); err == nil && home != "" {
		for _, candidate := range commonPathsFn(home) {
			attempts = append(attempts, candidate)
			if executableCheck(candidate) {
				return candidate, nil
			}
		}
	}

	return "", &CLINotFoundError{Attempts: attempts}
}

func defaultNPMGlobalBin(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "npm", "bin", "-g")
	out, err := cmd.Output()
	if err == nil {
		bin := strings.TrimSpace(string(out))
		if bin != "" {
			return bin, nil
		}
	}

	cmd = exec.CommandContext(ctx, "npm", "root", "-g")
	out, err = cmd.Output()
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", errors.New("npm root -g returned empty path")
	}
	return filepath.Join(filepath.Dir(root), "bin"), nil
}

func defaultCommonGeminiPaths(home string) []string {
	return []string{
		filepath.Join(home, ".npm-global", "bin", "gemini"),
		filepath.Join(home, ".local", "bin", "gemini"),
		"/usr/local/bin/gemini",
		"/opt/homebrew/bin/gemini",
		"/usr/bin/gemini",
	}
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

type stderrRing struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newStderrRing(max int) *stderrRing {
	if max <= 0 {
		max = defaultStderrBufferBytes
	}
	return &stderrRing{
		max: max,
		buf: make([]byte, 0, max),
	}
}

func (r *stderrRing) Write(p []byte) (int, error) {
	r.Append(p)
	return len(p), nil
}

func (r *stderrRing) Append(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(p) >= r.max {
		r.buf = append(r.buf[:0], p[len(p)-r.max:]...)
		return
	}

	overflow := len(r.buf) + len(p) - r.max
	if overflow > 0 {
		r.buf = append([]byte(nil), r.buf[overflow:]...)
	}
	r.buf = append(r.buf, p...)
}

func (r *stderrRing) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(append([]byte(nil), r.buf...))
}

func startStderrDrain(reader io.Reader, ring *stderrRing) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if reader == nil || ring == nil {
			return
		}
		_, _ = io.Copy(ring, reader)
	}()
	return done
}

func mergeEnv(extra []string) []string {
	if len(extra) == 0 {
		return os.Environ()
	}
	base := os.Environ()
	merged := make([]string, 0, len(base)+len(extra))
	merged = append(merged, base...)
	merged = append(merged, extra...)
	return merged
}

func prepareLaunchOptions(o options) (options, func(), error) {
	prepared := o
	prepared.args = append([]string(nil), o.args...)
	prepared.env = append([]string(nil), o.env...)
	prepared.allowedTools = append([]string(nil), o.allowedTools...)
	prepared.excludedTools = append([]string(nil), o.excludedTools...)
	prepared.addDirs = append([]string(nil), o.addDirs...)
	prepared.policyPaths = append([]string(nil), o.policyPaths...)

	if len(prepared.excludedTools) == 0 {
		return prepared, func() {}, nil
	}

	policyPath, cleanup, err := writeExcludedToolsPolicy(prepared.excludedTools)
	if err != nil {
		return options{}, nil, err
	}
	prepared.excludedTools = nil
	prepared.policyPaths = append(prepared.policyPaths, policyPath)
	return prepared, cleanup, nil
}

func buildCLIArgs(o options) []string {
	args := append([]string(nil), o.args...)
	if o.model != "" {
		args = append(args, "--model", o.model)
	}
	if o.sandbox != "" {
		args = append(args, "--sandbox")
	} else if o.sandboxEnabled != nil && *o.sandboxEnabled {
		args = append(args, "--sandbox")
	}
	if o.approvalMode != "" {
		args = append(args, "--approval-mode", o.approvalMode)
	}
	if len(o.allowedTools) > 0 {
		args = append(args, "--allowed-tools", strings.Join(o.allowedTools, ","))
	}
	if len(o.addDirs) > 0 {
		args = append(args, "--include-directories", strings.Join(o.addDirs, ","))
	}
	for _, policyPath := range o.policyPaths {
		policyPath = strings.TrimSpace(policyPath)
		if policyPath == "" {
			continue
		}
		args = append(args, "--policy", policyPath)
	}
	return args
}

func writeExcludedToolsPolicy(tools []string) (string, func(), error) {
	filtered := make([]string, 0, len(tools))
	seen := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			continue
		}
		if _, ok := seen[tool]; ok {
			continue
		}
		seen[tool] = struct{}{}
		filtered = append(filtered, tool)
	}
	if len(filtered) == 0 {
		return "", func() {}, nil
	}

	dir, err := os.MkdirTemp("", "go-gemini-sdk-policy-")
	if err != nil {
		return "", nil, fmt.Errorf("create excluded-tools policy dir: %w", err)
	}

	var b strings.Builder
	b.WriteString("# Generated by go-gemini-sdk to emulate excluded tools.\n")
	for i, tool := range filtered {
		fmt.Fprintf(&b, "\n[[rule]]\ntoolName = %s\ndecision = \"deny\"\npriority = %d\n", strconv.Quote(tool), 100+i)
	}

	path := filepath.Join(dir, "excluded-tools.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("write excluded-tools policy: %w", err)
	}

	cleanup := func() {
		_ = os.RemoveAll(dir)
	}
	return path, cleanup, nil
}

func processExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func wrapProcessError(op string, err error, stderrTail string) error {
	if err == nil {
		return nil
	}
	return &ProcessError{
		Op:         op,
		Err:        err,
		ExitCode:   processExitCode(err),
		StderrTail: stderrTail,
	}
}

func sanitizeStderrTail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 512 {
		return s
	}
	return fmt.Sprintf("...%s", s[len(s)-512:])
}
