//go:build integration

package gemini

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

func requireIntegrationEnv(t *testing.T) {
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
}
