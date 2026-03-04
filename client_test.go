package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

type testRunner struct {
	startFn func(ctx context.Context, binary string, args []string, env []string, cwd string) (*processHandle, error)
}

func (r *testRunner) Start(ctx context.Context, binary string, args []string, env []string, cwd string) (*processHandle, error) {
	if r.startFn == nil {
		return nil, errors.New("startFn not set")
	}
	return r.startFn(ctx, binary, args, env, cwd)
}

func TestClientConnectSendReceiveAndClose(t *testing.T) {
	runner := &testRunner{
		startFn: func(ctx context.Context, binary string, args []string, env []string, cwd string) (*processHandle, error) {
			return newMockACPProcess(t, mockACPConfig{}), nil
		},
	}

	client := NewClient(
		WithRunner(runner),
		WithBinaryPath("/tmp/mock-gemini"),
		WithRequestTimeout(time.Second),
		WithCloseTimeout(2*time.Second),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if got := client.SessionID(); got == "" {
		t.Fatal("SessionID() is empty")
	}

	events, errs := client.ReceiveWithErrors()
	for i := 1; i <= 3; i++ {
		prompt := fmt.Sprintf("round-%d", i)
		if err := client.Send(ctx, prompt); err != nil {
			t.Fatalf("Send(%q) error = %v", prompt, err)
		}

		turnEvents := waitTurnEvents(t, events, errs, 2*time.Second)
		if len(turnEvents) < 2 {
			t.Fatalf("turn events len = %d, want >= 2", len(turnEvents))
		}
		if turnEvents[0].Type != EventTypeMessageChunk {
			t.Fatalf("first event type = %q, want %q", turnEvents[0].Type, EventTypeMessageChunk)
		}
		wantText := "reply:" + prompt
		if turnEvents[0].Text != wantText {
			t.Fatalf("first event text = %q, want %q", turnEvents[0].Text, wantText)
		}
		last := turnEvents[len(turnEvents)-1]
		if !(last.Done || last.Type == EventTypeCompleted) {
			t.Fatalf("last event not completed: %+v", last)
		}
	}

	if err := client.CloseContext(ctx); err != nil {
		t.Fatalf("CloseContext() error = %v", err)
	}
}

func TestClientHandlesRequestPermission(t *testing.T) {
	got := runPermissionRoundtrip(
		t,
		`{"session_id":"s-1","tool_name":"bash","reason":"need shell","args":{"command":"rm -rf /tmp/data"},"options":[{"id":"allow_once"},{"id":"reject_once"}]}`,
		WithCanUseTool(func(ctx context.Context, call ToolCallInfo, options []PermissionOption) (string, error) {
			if call.ToolName != "bash" {
				t.Fatalf("tool_name = %q, want bash", call.ToolName)
			}
			if call.ToolKind != ToolKindBash {
				t.Fatalf("tool_kind = %q, want %q", call.ToolKind, ToolKindBash)
			}
			if len(options) != 2 {
				t.Fatalf("options len = %d, want 2", len(options))
			}
			return "reject_once", nil
		}),
	)

	if got.SelectedOptionID != "reject_once" {
		t.Fatalf("selected_option_id = %q, want %q", got.SelectedOptionID, "reject_once")
	}
}

func TestClientPermissionFallbackPrefersReject(t *testing.T) {
	got := runPermissionRoundtrip(
		t,
		`{"session_id":"s-1","tool_name":"bash","options":[{"id":"allow_once"},{"id":"reject_once"}]}`,
	)
	if got.SelectedOptionID != "reject_once" {
		t.Fatalf("selected_option_id = %q, want %q", got.SelectedOptionID, "reject_once")
	}
}

func TestClientPermissionFallbackAllowThenFirst(t *testing.T) {
	got := runPermissionRoundtrip(
		t,
		`{"session_id":"s-1","tool_name":"read","options":[{"id":"ask_once"},{"id":"allow_once"}]}`,
	)
	if got.SelectedOptionID != "allow_once" {
		t.Fatalf("selected_option_id = %q, want %q", got.SelectedOptionID, "allow_once")
	}

	got = runPermissionRoundtrip(
		t,
		`{"session_id":"s-1","tool_name":"read","options":[{"id":"ask_once"},{"id":"manual_review"}]}`,
	)
	if got.SelectedOptionID != "ask_once" {
		t.Fatalf("selected_option_id = %q, want %q", got.SelectedOptionID, "ask_once")
	}
}

func runPermissionRoundtrip(t *testing.T, permissionParams string, extraOpts ...Option) requestPermissionResult {
	t.Helper()

	permResultCh := make(chan requestPermissionResult, 1)

	runner := &testRunner{
		startFn: func(ctx context.Context, binary string, args []string, env []string, cwd string) (*processHandle, error) {
			cfg := mockACPConfig{
				onPrompt: func(state *mockACPState, req jsonrpcMessage, dec *json.Decoder, enc *json.Encoder) {
					_ = enc.Encode(jsonrpcMessage{
						JSONRPC: jsonrpcVersion,
						ID:      json.RawMessage("99"),
						Method:  methodSessionRequestPermission,
						Params:  json.RawMessage(permissionParams),
					})

					var permResp jsonrpcMessage
					if err := dec.Decode(&permResp); err == nil {
						var out requestPermissionResult
						if permResp.Error == nil {
							_ = json.Unmarshal(permResp.Result, &out)
							permResultCh <- out
						}
					}

					state.turn++
					_ = enc.Encode(jsonrpcMessage{
						JSONRPC: jsonrpcVersion,
						ID:      req.ID,
						Result:  mustRawJSON(t, sessionPromptResult{Accepted: true, TurnID: fmt.Sprintf("t-%d", state.turn)}),
					})
					_ = enc.Encode(jsonrpcMessage{
						JSONRPC: jsonrpcVersion,
						Method:  methodSessionUpdate,
						Params:  mustRawJSON(t, sessionUpdateParams{SessionID: "s-1", Type: string(EventTypeCompleted), TurnID: fmt.Sprintf("t-%d", state.turn), Done: true}),
					})
				},
			}
			return newMockACPProcess(t, cfg), nil
		},
	}

	clientOpts := []Option{
		WithRunner(runner),
		WithBinaryPath("/tmp/mock-gemini"),
		WithRequestTimeout(time.Second),
		WithCloseTimeout(2 * time.Second),
	}
	clientOpts = append(clientOpts, extraOpts...)
	client := NewClient(clientOpts...)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	events, errs := client.ReceiveWithErrors()
	if err := client.Send(ctx, "need-permission"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	_ = waitTurnEvents(t, events, errs, 2*time.Second)

	var out requestPermissionResult
	select {
	case out = <-permResultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting permission response")
	}

	if err := client.CloseContext(ctx); err != nil {
		t.Fatalf("CloseContext() error = %v", err)
	}
	return out
}

func waitTurnEvents(t *testing.T, events <-chan SessionEvent, errs <-chan error, timeout time.Duration) []SessionEvent {
	t.Helper()
	deadline := time.After(timeout)
	var out []SessionEvent

	for {
		select {
		case err, ok := <-errs:
			if !ok {
				err = nil
			}
			if err != nil {
				t.Fatalf("receive error: %v", err)
			}
		case ev, ok := <-events:
			if !ok {
				return out
			}
			out = append(out, ev)
			if ev.Done || ev.Type == EventTypeCompleted {
				return out
			}
		case <-deadline:
			t.Fatalf("timeout waiting events, collected=%d", len(out))
		}
	}
}

type mockACPConfig struct {
	onPrompt func(state *mockACPState, req jsonrpcMessage, dec *json.Decoder, enc *json.Encoder)
}

type mockACPState struct {
	turn int
}

func newMockACPProcess(t *testing.T, cfg mockACPConfig) *processHandle {
	t.Helper()

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	var once sync.Once
	done := make(chan struct{})
	finish := func() {
		once.Do(func() {
			_ = stdinR.Close()
			_ = stdoutW.Close()
			_ = stderrW.Close()
			close(done)
		})
	}

	go func() {
		defer finish()

		dec := json.NewDecoder(stdinR)
		enc := json.NewEncoder(stdoutW)
		state := &mockACPState{}

		for {
			var msg jsonrpcMessage
			if err := dec.Decode(&msg); err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
					t.Errorf("mock decode error: %v", err)
				}
				return
			}

			switch msg.Method {
			case methodInitialize:
				_ = enc.Encode(jsonrpcMessage{
					JSONRPC: jsonrpcVersion,
					ID:      msg.ID,
					Result:  mustRawJSON(t, initializeResult{ProtocolVersion: "1.0"}),
				})
			case methodSessionNew:
				_ = enc.Encode(jsonrpcMessage{
					JSONRPC: jsonrpcVersion,
					ID:      msg.ID,
					Result:  mustRawJSON(t, sessionNewResult{SessionID: "s-1"}),
				})
			case methodSessionPrompt:
				if cfg.onPrompt != nil {
					cfg.onPrompt(state, msg, dec, enc)
					continue
				}

				var in sessionPromptParams
				_ = json.Unmarshal(msg.Params, &in)
				state.turn++
				turnID := fmt.Sprintf("t-%d", state.turn)
				_ = enc.Encode(jsonrpcMessage{
					JSONRPC: jsonrpcVersion,
					ID:      msg.ID,
					Result:  mustRawJSON(t, sessionPromptResult{Accepted: true, TurnID: turnID}),
				})
				_ = enc.Encode(jsonrpcMessage{
					JSONRPC: jsonrpcVersion,
					Method:  methodSessionUpdate,
					Params: mustRawJSON(t, sessionUpdateParams{
						SessionID: "s-1",
						Type:      string(EventTypeMessageChunk),
						TurnID:    turnID,
						Text:      "reply:" + in.Prompt,
					}),
				})
				_ = enc.Encode(jsonrpcMessage{
					JSONRPC: jsonrpcVersion,
					Method:  methodSessionUpdate,
					Params: mustRawJSON(t, sessionUpdateParams{
						SessionID: "s-1",
						Type:      string(EventTypeCompleted),
						TurnID:    turnID,
						Done:      true,
					}),
				})
			case methodSessionInterrupt:
				// Best-effort; no response for notifications.
			default:
				if msg.hasID() {
					_ = enc.Encode(jsonrpcMessage{
						JSONRPC: jsonrpcVersion,
						ID:      msg.ID,
						Error: &jsonrpcError{
							Code:    -32601,
							Message: "method not found",
						},
					})
				}
			}
		}
	}()

	return &processHandle{
		PID:    12345,
		Stdin:  stdinW,
		Stdout: stdoutR,
		Stderr: stderrR,
		Wait: func() error {
			<-done
			return nil
		},
		Kill: func() error {
			finish()
			return nil
		},
		KillGroup: func() error {
			finish()
			return nil
		},
	}
}

func mustRawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return b
}
