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

func TestClientConnectQueryAndClose(t *testing.T) {
	client := newTestClient(t, mockACPConfig{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	for i := 1; i <= 3; i++ {
		prompt := fmt.Sprintf("round-%d", i)
		turn, err := client.Query(ctx, prompt)
		if err != nil {
			t.Fatalf("Query(%q) error = %v", prompt, err)
		}

		messages, recvErr := collectTurnMessages(t, turn, 2*time.Second)
		if recvErr != nil {
			t.Fatalf("turn recv error = %v", recvErr)
		}
		assertTurnText(t, messages, "reply:"+prompt)
	}

	if err := client.CloseContext(ctx); err != nil {
		t.Fatalf("CloseContext() error = %v", err)
	}
}

func TestClientQueryStreamsTypedMessages(t *testing.T) {
	cfg := mockACPConfig{
		onPrompt: func(state *mockACPState, req jsonrpcMessage, dec *json.Decoder, enc *json.Encoder) {
			state.turn++
			turnID := fmt.Sprintf("t-%d", state.turn)
			_ = enc.Encode(jsonrpcMessage{
				JSONRPC: jsonrpcVersion,
				ID:      req.ID,
				Result:  mustRawJSON(t, sessionPromptResult{Accepted: true, TurnID: turnID}),
			})
			_ = enc.Encode(jsonrpcMessage{
				JSONRPC: jsonrpcVersion,
				Method:  methodSessionUpdate,
				Params: mustRawJSON(t, sessionUpdateParams{
					SessionID: "s-1",
					Type:      "agent_thought_chunk",
					TurnID:    turnID,
					Text:      "先分析上下文",
				}),
			})
			_ = enc.Encode(jsonrpcMessage{
				JSONRPC: jsonrpcVersion,
				Method:  methodSessionUpdate,
				Params: mustRawJSON(t, sessionUpdateParams{
					SessionID:  "s-1",
					Type:       string(eventTypeToolCall),
					TurnID:     turnID,
					ToolName:   "bash",
					ToolCallID: "tool-1",
					Data:       mustRawJSON(t, map[string]any{"command": "ls -la"}),
				}),
			})
			_ = enc.Encode(jsonrpcMessage{
				JSONRPC: jsonrpcVersion,
				Method:  methodSessionUpdate,
				Params: mustRawJSON(t, sessionUpdateParams{
					SessionID:  "s-1",
					Type:       string(eventTypeToolCallUpdate),
					TurnID:     turnID,
					ToolName:   "bash",
					ToolCallID: "tool-1",
					Data:       mustRawJSON(t, map[string]any{"exitCode": 0, "stdout": "ok"}),
				}),
			})
			_ = enc.Encode(jsonrpcMessage{
				JSONRPC: jsonrpcVersion,
				Method:  methodSessionUpdate,
				Params: mustRawJSON(t, sessionUpdateParams{
					SessionID: "s-1",
					Type:      "agent_message_chunk",
					TurnID:    turnID,
					Text:      "执行完成",
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
		},
	}
	client := newTestClient(t, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	turn, err := client.Query(ctx, "do-work")
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	messages, recvErr := collectTurnMessages(t, turn, 2*time.Second)
	if recvErr != nil {
		t.Fatalf("collectTurnMessages() error = %v", recvErr)
	}
	if len(messages) != 5 {
		t.Fatalf("len(messages) = %d, want 5", len(messages))
	}
	if _, ok := messages[0].(*AssistantMessage); !ok {
		t.Fatalf("messages[0] = %T, want AssistantMessage", messages[0])
	}
	if block := firstContentBlock(t, messages[0]); block.contentBlockType() != "thinking" {
		t.Fatalf("messages[0] block type = %q, want thinking", block.contentBlockType())
	}
	if toolUse, ok := firstContentBlock(t, messages[1]).(*ToolUseBlock); !ok || toolUse.Name != "bash" {
		t.Fatalf("messages[1] = %#v, want ToolUseBlock bash", messages[1])
	}
	if toolResult, ok := firstContentBlock(t, messages[2]).(*ToolResultBlock); !ok || toolResult.ToolUseID != "tool-1" {
		t.Fatalf("messages[2] = %#v, want ToolResultBlock tool-1", messages[2])
	}
	if text, ok := firstContentBlock(t, messages[3]).(*TextBlock); !ok || text.Text != "执行完成" {
		t.Fatalf("messages[3] = %#v, want text block 执行完成", messages[3])
	}
	if result, ok := messages[4].(*ResultMessage); !ok || result.IsError {
		t.Fatalf("messages[4] = %#v, want successful ResultMessage", messages[4])
	}
}

func TestClientQueryReturnsPromptErrorOnTurn(t *testing.T) {
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
	client := newTestClient(t, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	turn, err := client.Query(ctx, "hello")
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	messages, recvErr := collectTurnMessages(t, turn, 2*time.Second)
	if len(messages) != 0 {
		t.Fatalf("len(messages) = %d, want 0", len(messages))
	}
	if recvErr == nil {
		t.Fatal("turn error = nil, want non-nil")
	}
	var pErr *ProtocolError
	if !errors.As(recvErr, &pErr) {
		t.Fatalf("turn error = %T, want ProtocolError", recvErr)
	}
}

func TestClientQueryReturnsBeforePromptResult(t *testing.T) {
	cfg := mockACPConfig{
		onPrompt: func(state *mockACPState, req jsonrpcMessage, dec *json.Decoder, enc *json.Encoder) {
			state.turn++
			turnID := fmt.Sprintf("t-%d", state.turn)
			for i := 0; i < defaultEventBuffer+2; i++ {
				_ = enc.Encode(jsonrpcMessage{
					JSONRPC: jsonrpcVersion,
					Method:  methodSessionUpdate,
					Params: mustRawJSON(t, sessionUpdateParams{
						SessionID: "s-1",
						Type:      string(eventTypeMessageChunk),
						TurnID:    turnID,
						Text:      fmt.Sprintf("chunk-%03d", i),
					}),
				})
			}
			_ = enc.Encode(jsonrpcMessage{
				JSONRPC: jsonrpcVersion,
				ID:      req.ID,
				Result: mustRawJSON(t, sessionPromptResult{
					Accepted:   true,
					TurnID:     turnID,
					StopReason: "end_turn",
				}),
			})
		},
	}
	client := NewClient(
		WithRunner(&testRunner{
			startFn: func(ctx context.Context, binary string, args []string, env []string, cwd string) (*processHandle, error) {
				return newMockACPProcess(t, cfg), nil
			},
		}),
		WithBinaryPath("/tmp/mock-gemini"),
		WithRequestTimeout(time.Second),
		WithCloseTimeout(2*time.Second),
		WithEventBuffer(1),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	start := time.Now()
	turn, err := client.Query(ctx, "hello")
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Query() blocked for %s, want quick submit return", elapsed)
	}

	messages, recvErr := collectTurnMessages(t, turn, 3*time.Second)
	if recvErr != nil {
		t.Fatalf("turn recv error = %v", recvErr)
	}
	if len(messages) != defaultEventBuffer+3 {
		t.Fatalf("len(messages) = %d, want %d", len(messages), defaultEventBuffer+3)
	}
	if text, ok := firstContentBlock(t, messages[0]).(*TextBlock); !ok || text.Text != "chunk-000" {
		t.Fatalf("messages[0] = %#v, want first text chunk", messages[0])
	}
	if result, ok := messages[len(messages)-1].(*ResultMessage); !ok || result.StopReason != "end_turn" {
		t.Fatalf("last message = %#v, want ResultMessage stopReason=end_turn", messages[len(messages)-1])
	}
}

func TestClientQueryRejectsConcurrentTurns(t *testing.T) {
	release := make(chan struct{})
	cfg := mockACPConfig{
		onPrompt: func(state *mockACPState, req jsonrpcMessage, dec *json.Decoder, enc *json.Encoder) {
			<-release
			state.turn++
			_ = enc.Encode(jsonrpcMessage{
				JSONRPC: jsonrpcVersion,
				ID:      req.ID,
				Result:  mustRawJSON(t, sessionPromptResult{Accepted: true, TurnID: fmt.Sprintf("t-%d", state.turn), StopReason: "end_turn"}),
			})
		},
	}
	client := newTestClient(t, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	turn, err := client.Query(ctx, "hello")
	if err != nil {
		t.Fatalf("first Query() error = %v", err)
	}
	_, err = client.Query(ctx, "world")
	if err == nil {
		t.Fatal("second Query() error = nil, want non-nil")
	}
	var pErr *ProtocolError
	if !errors.As(err, &pErr) {
		t.Fatalf("second Query() error = %T, want ProtocolError", err)
	}
	close(release)
	if _, recvErr := collectTurnMessages(t, turn, 2*time.Second); recvErr != nil {
		t.Fatalf("first turn recv error = %v", recvErr)
	}
}

func TestClientQueryReturnsConnectionInactiveError(t *testing.T) {
	client := NewClient()
	rpcConn := &conn{done: make(chan struct{})}
	close(rpcConn.done)

	client.mu.Lock()
	client.conn = rpcConn
	client.sessionID = "s-1"
	client.connected = true
	client.processErr = &ProcessError{
		Op:         "wait",
		ExitCode:   17,
		StderrTail: "panic: boom",
		Err:        errors.New("exit status 17"),
	}
	client.mu.Unlock()

	_, err := client.Query(context.Background(), "hello")
	assertConnectionInactiveError(t, err, "query", 17, "panic: boom")
}

func TestClientInterruptReturnsConnectionInactiveError(t *testing.T) {
	client := NewClient()
	rpcConn := &conn{done: make(chan struct{})}
	close(rpcConn.done)

	client.mu.Lock()
	client.conn = rpcConn
	client.sessionID = "s-1"
	client.connected = true
	client.processErr = &ProcessError{
		Op:         "wait",
		ExitCode:   9,
		StderrTail: "killed",
		Err:        errors.New("exit status 9"),
	}
	client.mu.Unlock()

	err := client.Interrupt(context.Background())
	assertConnectionInactiveError(t, err, "interrupt", 9, "killed")
}

func TestClientInterruptWhenNotConnectedReturnsError(t *testing.T) {
	client := NewClient()

	err := client.Interrupt(context.Background())
	if err == nil {
		t.Fatal("Interrupt() error = nil, want non-nil")
	}
	if !errors.Is(err, ErrConnectionInactive) {
		t.Fatalf("Interrupt() error = %v, want ErrConnectionInactive", err)
	}
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Interrupt() error = %v, want io.ErrClosedPipe", err)
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

	if got.Outcome == nil || got.Outcome.OptionID != "reject_once" {
		gotID := ""
		if got.Outcome != nil {
			gotID = got.Outcome.OptionID
		}
		t.Fatalf("outcome.optionId = %q, want %q", gotID, "reject_once")
	}
}

func TestClientPermissionFallbackPrefersReject(t *testing.T) {
	got := runPermissionRoundtrip(
		t,
		`{"session_id":"s-1","tool_name":"bash","options":[{"id":"allow_once"},{"id":"reject_once"}]}`,
	)
	if got.Outcome == nil || got.Outcome.OptionID != "reject_once" {
		gotID := ""
		if got.Outcome != nil {
			gotID = got.Outcome.OptionID
		}
		t.Fatalf("outcome.optionId = %q, want %q", gotID, "reject_once")
	}
}

func TestClientPermissionFallbackAllowThenFirst(t *testing.T) {
	got := runPermissionRoundtrip(
		t,
		`{"session_id":"s-1","tool_name":"read","options":[{"id":"ask_once"},{"id":"allow_once"}]}`,
	)
	if got.Outcome == nil || got.Outcome.OptionID != "allow_once" {
		gotID := ""
		if got.Outcome != nil {
			gotID = got.Outcome.OptionID
		}
		t.Fatalf("outcome.optionId = %q, want %q", gotID, "allow_once")
	}

	got = runPermissionRoundtrip(
		t,
		`{"session_id":"s-1","tool_name":"read","options":[{"id":"ask_once"},{"id":"manual_review"}]}`,
	)
	if got.Outcome == nil || got.Outcome.OptionID != "ask_once" {
		gotID := ""
		if got.Outcome != nil {
			gotID = got.Outcome.OptionID
		}
		t.Fatalf("outcome.optionId = %q, want %q", gotID, "ask_once")
	}
}

func runPermissionRoundtrip(t *testing.T, permissionParams string, extraOpts ...Option) requestPermissionResult {
	t.Helper()

	permResultCh := make(chan requestPermissionResult, 1)

	cfg := mockACPConfig{
		onPrompt: func(state *mockACPState, req jsonrpcMessage, dec *json.Decoder, enc *json.Encoder) {
			_ = enc.Encode(jsonrpcMessage{
				JSONRPC: jsonrpcVersion,
				ID:      json.RawMessage("99"),
				Method:  methodSessionRequestPermission,
				Params:  json.RawMessage(permissionParams),
			})

			var permResp jsonrpcMessage
			if err := dec.Decode(&permResp); err == nil && permResp.Error == nil {
				var out requestPermissionResult
				_ = json.Unmarshal(permResp.Result, &out)
				permResultCh <- out
			}

			state.turn++
			_ = enc.Encode(jsonrpcMessage{
				JSONRPC: jsonrpcVersion,
				ID:      req.ID,
				Result: mustRawJSON(t, sessionPromptResult{
					Accepted:   true,
					TurnID:     fmt.Sprintf("t-%d", state.turn),
					StopReason: "end_turn",
				}),
			})
		},
	}

	clientOpts := []Option{
		WithRunner(&testRunner{
			startFn: func(ctx context.Context, binary string, args []string, env []string, cwd string) (*processHandle, error) {
				return newMockACPProcess(t, cfg), nil
			},
		}),
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

	turn, err := client.Query(ctx, "need-permission")
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if _, recvErr := collectTurnMessages(t, turn, 2*time.Second); recvErr != nil {
		t.Fatalf("turn recv error = %v", recvErr)
	}

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

func newTestClient(t *testing.T, cfg mockACPConfig) *Client {
	t.Helper()
	return NewClient(
		WithRunner(&testRunner{
			startFn: func(ctx context.Context, binary string, args []string, env []string, cwd string) (*processHandle, error) {
				return newMockACPProcess(t, cfg), nil
			},
		}),
		WithBinaryPath("/tmp/mock-gemini"),
		WithRequestTimeout(time.Second),
		WithCloseTimeout(2*time.Second),
	)
}

func assertTurnText(t *testing.T, messages []Message, want string) {
	t.Helper()
	if len(messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(messages))
	}
	assistant, ok := messages[0].(*AssistantMessage)
	if !ok {
		t.Fatalf("messages[0] = %T, want AssistantMessage", messages[0])
	}
	text, ok := firstContentBlock(t, assistant).(*TextBlock)
	if !ok || text.Text != want {
		t.Fatalf("messages[0] text = %#v, want %q", messages[0], want)
	}
	result, ok := messages[1].(*ResultMessage)
	if !ok || result.IsError {
		t.Fatalf("messages[1] = %#v, want successful ResultMessage", messages[1])
	}
}

func firstContentBlock(t *testing.T, msg Message) ContentBlock {
	t.Helper()
	assistant, ok := msg.(*AssistantMessage)
	if !ok {
		t.Fatalf("msg = %T, want AssistantMessage", msg)
	}
	if len(assistant.Content) != 1 {
		t.Fatalf("len(Content) = %d, want 1", len(assistant.Content))
	}
	return assistant.Content[0]
}

func collectTurnMessages(t *testing.T, turn *TurnHandle, timeout time.Duration) ([]Message, error) {
	t.Helper()

	deadline := time.After(timeout)
	messages := turn.Messages()
	errs := turn.Errors()
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
			t.Fatalf("timeout collecting turn messages, collected=%d", len(out))
		}
	}

	return out, nil
}

func assertConnectionInactiveError(t *testing.T, err error, op string, exitCode int, stderrTail string) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want non-nil")
	}
	if !errors.Is(err, ErrConnectionInactive) {
		t.Fatalf("error = %v, want ErrConnectionInactive", err)
	}
	if !errors.Is(err, ErrProcess) {
		t.Fatalf("error = %v, want ErrProcess", err)
	}

	var inactiveErr *ConnectionInactiveError
	if !errors.As(err, &inactiveErr) {
		t.Fatalf("error type = %T, want ConnectionInactiveError", err)
	}
	if inactiveErr.Op != op {
		t.Fatalf("inactiveErr.Op = %q, want %q", inactiveErr.Op, op)
	}

	var processErr *ProcessError
	if !errors.As(err, &processErr) {
		t.Fatalf("error = %v, want ProcessError in unwrap chain", err)
	}
	if processErr.ExitCode != exitCode {
		t.Fatalf("processErr.ExitCode = %d, want %d", processErr.ExitCode, exitCode)
	}
	if processErr.StderrTail != stderrTail {
		t.Fatalf("processErr.StderrTail = %q, want %q", processErr.StderrTail, stderrTail)
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
					Result:  mustRawJSON(t, initializeResult{ProtocolVersion: 1}),
				})
			case methodSessionNew:
				_ = enc.Encode(jsonrpcMessage{
					JSONRPC: jsonrpcVersion,
					ID:      msg.ID,
					Result:  mustRawJSON(t, sessionNewResult{SessionIDV2: "s-1"}),
				})
			case methodSessionPrompt:
				if cfg.onPrompt != nil {
					cfg.onPrompt(state, msg, dec, enc)
					continue
				}

				var in sessionPromptParams
				_ = json.Unmarshal(msg.Params, &in)
				promptText := ""
				for _, block := range in.Prompt {
					if block.Type == "text" {
						promptText += block.Text
					}
				}
				if promptText == "" {
					promptText = "unknown"
				}
				state.turn++
				turnID := fmt.Sprintf("t-%d", state.turn)
				_ = enc.Encode(jsonrpcMessage{
					JSONRPC: jsonrpcVersion,
					Method:  methodSessionUpdate,
					Params: mustRawJSON(t, sessionUpdateParams{
						SessionID: "s-1",
						Type:      string(eventTypeMessageChunk),
						TurnID:    turnID,
						Text:      "reply:" + promptText,
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
					ID:      msg.ID,
					Result:  mustRawJSON(t, sessionPromptResult{Accepted: true, TurnID: turnID}),
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
