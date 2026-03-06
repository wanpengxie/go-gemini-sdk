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

func TestClientReceiveBlocksWithErrors(t *testing.T) {
	runner := &testRunner{
		startFn: func(ctx context.Context, binary string, args []string, env []string, cwd string) (*processHandle, error) {
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
							Type:       string(EventTypeToolCall),
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
							Type:       string(EventTypeToolCallUpdate),
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
							Type:      string(EventTypeCompleted),
							TurnID:    turnID,
							Done:      true,
						}),
					})
				},
			}
			return newMockACPProcess(t, cfg), nil
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

	blocks, errs := client.ReceiveBlocksWithErrors()
	if err := client.Send(ctx, "do-work"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	turnBlocks := waitTurnBlocks(t, blocks, errs, 2*time.Second)
	if len(turnBlocks) != 5 {
		t.Fatalf("turn blocks len = %d, want 5", len(turnBlocks))
	}
	if turnBlocks[0].Kind != BlockKindThinking {
		t.Fatalf("block[0].Kind = %q, want %q", turnBlocks[0].Kind, BlockKindThinking)
	}
	if turnBlocks[0].RawType != "agent_thought_chunk" {
		t.Fatalf("block[0].RawType = %q, want agent_thought_chunk", turnBlocks[0].RawType)
	}
	if turnBlocks[1].Kind != BlockKindToolCall {
		t.Fatalf("block[1].Kind = %q, want %q", turnBlocks[1].Kind, BlockKindToolCall)
	}
	if turnBlocks[2].Kind != BlockKindToolResult {
		t.Fatalf("block[2].Kind = %q, want %q", turnBlocks[2].Kind, BlockKindToolResult)
	}
	if turnBlocks[3].Kind != BlockKindText {
		t.Fatalf("block[3].Kind = %q, want %q", turnBlocks[3].Kind, BlockKindText)
	}
	if turnBlocks[4].Kind != BlockKindDone {
		t.Fatalf("block[4].Kind = %q, want %q", turnBlocks[4].Kind, BlockKindDone)
	}

	if err := client.CloseContext(ctx); err != nil {
		t.Fatalf("CloseContext() error = %v", err)
	}
}

func TestClientReceiveMessagesAlias(t *testing.T) {
	runner := &testRunner{
		startFn: func(ctx context.Context, binary string, args []string, env []string, cwd string) (*processHandle, error) {
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
							Type:      string(EventTypeCompleted),
							TurnID:    turnID,
							Done:      true,
						}),
					})
				},
			}
			return newMockACPProcess(t, cfg), nil
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

	msgs, errs := client.ReceiveMessagesWithErrors()
	if err := client.Send(ctx, "ping"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	turn := waitTurnBlocks(t, msgs, errs, 2*time.Second)
	if len(turn) != 2 {
		t.Fatalf("turn blocks len = %d, want 2", len(turn))
	}
	if turn[0].Kind != BlockKindText {
		t.Fatalf("turn[0].Kind = %q, want %q", turn[0].Kind, BlockKindText)
	}
	if turn[1].Kind != BlockKindDone {
		t.Fatalf("turn[1].Kind = %q, want %q", turn[1].Kind, BlockKindDone)
	}

	if err := client.CloseContext(ctx); err != nil {
		t.Fatalf("CloseContext() error = %v", err)
	}
}

func TestClientEmitEventBackpressure(t *testing.T) {
	client := NewClient(WithEventBuffer(1))

	client.eventsCh <- SessionEvent{
		Type: EventTypeMessageChunk,
		Text: "first",
	}

	blockedDone := make(chan struct{})
	go func() {
		defer close(blockedDone)
		client.emitEvent(SessionEvent{
			Type: EventTypeCompleted,
			Done: true,
		})
	}()

	select {
	case <-blockedDone:
		t.Fatal("emitEvent should block when events channel is full")
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case ev := <-client.eventsCh:
		if ev.Text != "first" {
			t.Fatalf("first event text = %q, want first", ev.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting first event")
	}

	select {
	case <-blockedDone:
	case <-time.After(time.Second):
		t.Fatal("emitEvent did not unblock after draining events channel")
	}

	select {
	case ev := <-client.eventsCh:
		if ev.Type != EventTypeCompleted || !ev.Done {
			t.Fatalf("second event = %+v, want completed done=true", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting second event")
	}
}

func TestClientReceiveBlocksWithErrorsNoDropOnBackpressure(t *testing.T) {
	client := NewClient(WithEventBuffer(1))
	blocks, errs := client.ReceiveBlocksWithErrors()

	client.eventsCh <- SessionEvent{
		Type: EventTypeMessageChunk,
		Text: "first",
	}
	client.eventsCh <- SessionEvent{
		Type: EventTypeCompleted,
		Done: true,
	}
	close(client.eventsCh)

	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("unexpected receive error: %v", err)
		}
	case <-time.After(120 * time.Millisecond):
	}

	select {
	case block := <-blocks:
		if block.Kind != BlockKindText || block.Text != "first" {
			t.Fatalf("block[0] = %+v, want text block", block)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting first block")
	}

	select {
	case block := <-blocks:
		if block.Kind != BlockKindDone || !block.Done {
			t.Fatalf("block[1] = %+v, want done block", block)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting second block")
	}

	select {
	case _, ok := <-blocks:
		if ok {
			t.Fatal("blocks channel still open, want closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting blocks channel close")
	}
}

func TestClientSendReturnsConnectionInactiveError(t *testing.T) {
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

	err := client.Send(context.Background(), "hello")
	assertConnectionInactiveError(t, err, "send", 17, "panic: boom")
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

func waitTurnBlocks(t *testing.T, blocks <-chan StreamBlock, errs <-chan error, timeout time.Duration) []StreamBlock {
	t.Helper()
	deadline := time.After(timeout)
	var out []StreamBlock

	for {
		select {
		case err, ok := <-errs:
			if !ok {
				err = nil
			}
			if err != nil {
				t.Fatalf("receive error: %v", err)
			}
		case block, ok := <-blocks:
			if !ok {
				return out
			}
			out = append(out, block)
			if block.Done || block.Kind == BlockKindDone || block.Kind == BlockKindError {
				return out
			}
		case <-deadline:
			t.Fatalf("timeout waiting blocks, collected=%d", len(out))
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
						Text:      "reply:" + promptText,
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
