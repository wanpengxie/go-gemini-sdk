package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestQuerySuccess(t *testing.T) {
	runner := &testRunner{
		startFn: func(ctx context.Context, binary string, args []string, env []string, cwd string) (*processHandle, error) {
			return newMockACPProcess(t, mockACPConfig{}), nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := Query(ctx, "hello", WithRunner(runner), WithBinaryPath("/tmp/mock-gemini"))
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if got != "reply:hello" {
		t.Fatalf("Query() = %q, want %q", got, "reply:hello")
	}
}

func TestQueryReturnsPromptError(t *testing.T) {
	runner := &testRunner{
		startFn: func(ctx context.Context, binary string, args []string, env []string, cwd string) (*processHandle, error) {
			cfg := mockACPConfig{
				onPrompt: func(state *mockACPState, req jsonrpcMessage, dec *json.Decoder, enc *json.Encoder) {
					_ = enc.Encode(jsonrpcMessage{
						JSONRPC: jsonrpcVersion,
						ID:      req.ID,
						Error: &jsonrpcError{
							Code:    -32001,
							Message: "prompt rejected",
						},
					})
				},
			}
			return newMockACPProcess(t, cfg), nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := Query(ctx, "hello", WithRunner(runner), WithBinaryPath("/tmp/mock-gemini"))
	if err == nil {
		t.Fatal("Query() error = nil, want non-nil")
	}
	var pErr *ProtocolError
	if !errors.As(err, &pErr) {
		t.Fatalf("Query() error = %T, want ProtocolError", err)
	}
}
