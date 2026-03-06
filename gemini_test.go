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

func TestQueryBlocksSuccess(t *testing.T) {
	runner := &testRunner{
		startFn: func(ctx context.Context, binary string, args []string, env []string, cwd string) (*processHandle, error) {
			return newMockACPProcess(t, mockACPConfig{}), nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	blocksCh, errsCh, err := QueryBlocks(ctx, "hello", WithRunner(runner), WithBinaryPath("/tmp/mock-gemini"))
	if err != nil {
		t.Fatalf("QueryBlocks() error = %v", err)
	}

	blocks, recvErr := collectBlocksResult(t, blocksCh, errsCh, 3*time.Second)
	if recvErr != nil {
		t.Fatalf("QueryBlocks() recv error = %v", recvErr)
	}
	if len(blocks) != 2 {
		t.Fatalf("QueryBlocks() len = %d, want 2", len(blocks))
	}
	if blocks[0].Kind != BlockKindText || blocks[0].Text != "reply:hello" {
		t.Fatalf("blocks[0] = %+v, want text block reply:hello", blocks[0])
	}
	if blocks[1].Kind != BlockKindDone {
		t.Fatalf("blocks[1].Kind = %q, want %q", blocks[1].Kind, BlockKindDone)
	}
}

func TestQueryBlocksReturnsPromptError(t *testing.T) {
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

	blocksCh, errsCh, err := QueryBlocks(ctx, "hello", WithRunner(runner), WithBinaryPath("/tmp/mock-gemini"))
	if err != nil {
		t.Fatalf("QueryBlocks() error = %v", err)
	}

	blocks, recvErr := collectBlocksResult(t, blocksCh, errsCh, 3*time.Second)
	if len(blocks) != 0 {
		t.Fatalf("QueryBlocks() blocks len = %d, want 0", len(blocks))
	}
	if recvErr == nil {
		t.Fatal("QueryBlocks() recv error = nil, want non-nil")
	}
	var pErr *ProtocolError
	if !errors.As(recvErr, &pErr) {
		t.Fatalf("QueryBlocks() recv error = %T, want ProtocolError", recvErr)
	}
}

func TestQueryBlocksContextCanceled(t *testing.T) {
	runner := &testRunner{
		startFn: func(ctx context.Context, binary string, args []string, env []string, cwd string) (*processHandle, error) {
			cfg := mockACPConfig{
				onPrompt: func(state *mockACPState, req jsonrpcMessage, dec *json.Decoder, enc *json.Encoder) {
					<-ctx.Done()
				},
			}
			return newMockACPProcess(t, cfg), nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	blocksCh, errsCh, err := QueryBlocks(ctx, "hello", WithRunner(runner), WithBinaryPath("/tmp/mock-gemini"))
	if err != nil {
		t.Fatalf("QueryBlocks() error = %v", err)
	}
	cancel()

	_, recvErr := collectBlocksResult(t, blocksCh, errsCh, 3*time.Second)
	if recvErr == nil {
		t.Fatal("QueryBlocks() recv error = nil, want non-nil")
	}
	if !errors.Is(recvErr, context.Canceled) {
		t.Fatalf("QueryBlocks() recv error = %v, want context.Canceled", recvErr)
	}
}

func collectBlocksResult(t *testing.T, blocks <-chan StreamBlock, errs <-chan error, timeout time.Duration) ([]StreamBlock, error) {
	t.Helper()

	deadline := time.After(timeout)
	var out []StreamBlock

	for blocks != nil || errs != nil {
		select {
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return out, err
			}
		case block, ok := <-blocks:
			if !ok {
				blocks = nil
				continue
			}
			out = append(out, block)
		case <-deadline:
			t.Fatalf("timeout collecting blocks, collected=%d", len(out))
		}
	}

	return out, nil
}
