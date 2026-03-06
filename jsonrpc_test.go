package gemini

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestConnCallAndResponse(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()

	rpcConn := newConn(clientSide, defaultMaxEventBytes)
	defer rpcConn.close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		dec := json.NewDecoder(serverSide)
		enc := json.NewEncoder(serverSide)

		var req jsonrpcMessage
		if err := dec.Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if req.Method != "ping" {
			t.Errorf("method = %q, want ping", req.Method)
			return
		}

		_ = enc.Encode(jsonrpcMessage{
			JSONRPC: jsonrpcVersion,
			ID:      req.ID,
			Result:  json.RawMessage(`{"ok":true}`),
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var out struct {
		OK bool `json:"ok"`
	}
	if err := rpcConn.call(ctx, "ping", map[string]any{"value": 1}, &out); err != nil {
		t.Fatalf("call() error = %v", err)
	}
	if !out.OK {
		t.Fatalf("call() result ok = false, want true")
	}

	<-done
}

func TestConnNotificationDispatch(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()

	rpcConn := newConn(clientSide, defaultMaxEventBytes)
	defer rpcConn.close()

	enc := json.NewEncoder(serverSide)
	if err := enc.Encode(jsonrpcMessage{
		JSONRPC: jsonrpcVersion,
		Method:  methodSessionUpdate,
		Params:  json.RawMessage(`{"type":"message_chunk","text":"hello"}`),
	}); err != nil {
		t.Fatalf("encode notification: %v", err)
	}

	select {
	case msg := <-rpcConn.Notifications():
		if msg.Method != methodSessionUpdate {
			t.Fatalf("notification method = %q, want %q", msg.Method, methodSessionUpdate)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting notification")
	}
}

func TestConnRequestHandler(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()

	rpcConn := newConn(clientSide, defaultMaxEventBytes)
	defer rpcConn.close()

	rpcConn.registerHandler(methodRequestPermission, func(ctx context.Context, raw json.RawMessage) (any, error) {
		return requestPermissionResult{
			Outcome: &requestPermissionOutcome{
				Outcome:  "selected",
				OptionID: "allow_once",
			},
		}, nil
	})

	dec := json.NewDecoder(serverSide)
	enc := json.NewEncoder(serverSide)

	if err := enc.Encode(jsonrpcMessage{
		JSONRPC: jsonrpcVersion,
		ID:      json.RawMessage("7"),
		Method:  methodRequestPermission,
		Params:  json.RawMessage(`{"tool_name":"bash"}`),
	}); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	var resp jsonrpcMessage
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if string(resp.ID) != "7" {
		t.Fatalf("response id = %s, want 7", string(resp.ID))
	}
	if resp.Error != nil {
		t.Fatalf("response error = %+v", resp.Error)
	}

	var result requestPermissionResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.Outcome == nil || result.Outcome.OptionID != "allow_once" {
		gotID := ""
		if result.Outcome != nil {
			gotID = result.Outcome.OptionID
		}
		t.Fatalf("outcome.optionId = %q, want %q", gotID, "allow_once")
	}
}

func TestConnCloseUnblocksPendingCall(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()

	rpcConn := newConn(clientSide, defaultMaxEventBytes)
	defer rpcConn.close()

	dec := json.NewDecoder(serverSide)
	requestSeen := make(chan struct{})
	go func() {
		var req jsonrpcMessage
		_ = dec.Decode(&req)
		close(requestSeen)
	}()

	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		errCh <- rpcConn.call(ctx, "slow", map[string]any{"v": 1}, nil)
	}()

	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting request")
	}

	rpcConn.close()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("call() error = nil, want non-nil")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting pending call to unblock")
	}
}
