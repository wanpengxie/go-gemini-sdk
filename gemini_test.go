package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestQuerySuccess(t *testing.T) {
	clientRunner := &testRunner{
		startFn: func(ctx context.Context, binary string, args []string, env []string, cwd string) (*processHandle, error) {
			return newMockACPProcess(t, mockACPConfig{}), nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	messages, errs := Query(ctx, "hello", WithRunner(clientRunner), WithBinaryPath("/tmp/mock-gemini"))
	got, err := collectQueryMessages(t, messages, errs, 3*time.Second)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	assertTurnText(t, got, "reply:hello")
}

func TestQueryReturnsPromptError(t *testing.T) {
	clientRunner := &testRunner{
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

	messages, errs := Query(ctx, "hello", WithRunner(clientRunner), WithBinaryPath("/tmp/mock-gemini"))
	got, err := collectQueryMessages(t, messages, errs, 3*time.Second)
	if len(got) != 0 {
		t.Fatalf("len(messages) = %d, want 0", len(got))
	}
	if err == nil {
		t.Fatal("Query() error = nil, want non-nil")
	}
	var pErr *ProtocolError
	if !errors.As(err, &pErr) {
		t.Fatalf("Query() error = %T, want ProtocolError", err)
	}
}

func TestQueryStreamsTypedMessages(t *testing.T) {
	clientRunner := &testRunner{
		startFn: func(ctx context.Context, binary string, args []string, env []string, cwd string) (*processHandle, error) {
			cfg := mockACPConfig{
				onPrompt: func(state *mockACPState, req jsonrpcMessage, dec *json.Decoder, enc *json.Encoder) {
					state.turn++
					turnID := "t-1"
					_ = enc.Encode(jsonrpcMessage{
						JSONRPC: jsonrpcVersion,
						Method:  methodSessionUpdate,
						Params: mustRawJSON(t, sessionUpdateParams{
							SessionID: "s-1",
							Type:      "agent_message_chunk",
							TurnID:    turnID,
							Text:      "hello",
						}),
					})
					_ = enc.Encode(jsonrpcMessage{
						JSONRPC: jsonrpcVersion,
						Method:  methodSessionUpdate,
						Params: mustRawJSON(t, sessionUpdateParams{
							SessionID: "s-1",
							Type:      string(eventTypeCompleted),
							TurnID:    turnID,
							Done:      true,
						}),
					})
					_ = enc.Encode(jsonrpcMessage{
						JSONRPC: jsonrpcVersion,
						ID:      req.ID,
						Result:  mustRawJSON(t, sessionPromptResult{Accepted: true, TurnID: turnID}),
					})
				},
			}
			return newMockACPProcess(t, cfg), nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	messages, errs := Query(ctx, "hello", WithRunner(clientRunner), WithBinaryPath("/tmp/mock-gemini"))
	got, err := collectQueryMessages(t, messages, errs, 3*time.Second)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(got))
	}
}

func TestQueryContextCanceled(t *testing.T) {
	clientRunner := &testRunner{
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
	messages, errs := Query(ctx, "hello", WithRunner(clientRunner), WithBinaryPath("/tmp/mock-gemini"))
	cancel()

	got, err := collectQueryMessages(t, messages, errs, 3*time.Second)
	if len(got) != 0 {
		t.Fatalf("len(messages) = %d, want 0", len(got))
	}
	if err == nil {
		t.Fatal("Query() error = nil, want non-nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Query() error = %v, want context.Canceled", err)
	}
}

func collectQueryMessages(t *testing.T, messages <-chan Message, errs <-chan error, timeout time.Duration) ([]Message, error) {
	t.Helper()

	deadline := time.After(timeout)
	var out []Message

	for messages != nil || errs != nil {
		select {
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return out, err
			}
		case msg, ok := <-messages:
			if !ok {
				messages = nil
				continue
			}
			out = append(out, msg)
		case <-deadline:
			t.Fatalf("timeout collecting query messages, collected=%d", len(out))
		}
	}

	return out, nil
}

func TestDecodeJSONValueFallback(t *testing.T) {
	raw := json.RawMessage(`{"x":1}`)
	got := decodeJSONValue(raw)
	if _, ok := got.(map[string]any); !ok {
		t.Fatalf("decodeJSONValue() = %T, want map[string]any", got)
	}
}
