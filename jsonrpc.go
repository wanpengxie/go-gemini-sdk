package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	jsonrpcVersion = "2.0"
)

type jsonrpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type jsonrpcMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

func (m jsonrpcMessage) hasID() bool {
	return len(bytesTrimSpace(m.ID)) > 0 && string(bytesTrimSpace(m.ID)) != "null"
}

func (m jsonrpcMessage) idKey() string {
	return string(bytesTrimSpace(m.ID))
}

type rpcHandler func(context.Context, json.RawMessage) (any, error)

type conn struct {
	stream io.ReadWriteCloser
	enc    *json.Encoder
	dec    *json.Decoder

	writeMu sync.Mutex

	mu       sync.Mutex
	pending  map[string]chan jsonrpcMessage
	handlers map[string]rpcHandler

	nextID uint64

	maxEventBytes int

	notifyCh chan jsonrpcMessage
	errCh    chan error
	done     chan struct{}

	closeOnce sync.Once
}

func newConn(stream io.ReadWriteCloser, maxEventBytes int) *conn {
	c := &conn{
		stream:        stream,
		enc:           json.NewEncoder(stream),
		dec:           json.NewDecoder(stream),
		pending:       make(map[string]chan jsonrpcMessage),
		handlers:      make(map[string]rpcHandler),
		maxEventBytes: maxEventBytes,
		notifyCh:      make(chan jsonrpcMessage, defaultEventBuffer),
		errCh:         make(chan error, 8),
		done:          make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *conn) Notifications() <-chan jsonrpcMessage {
	return c.notifyCh
}

func (c *conn) Errors() <-chan error {
	return c.errCh
}

func (c *conn) Done() <-chan struct{} {
	return c.done
}

func (c *conn) registerHandler(method string, handler rpcHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if method == "" || handler == nil {
		return
	}
	c.handlers[method] = handler
}

func (c *conn) call(ctx context.Context, method string, params any, out any) error {
	if method == "" {
		return wrapOp("jsonrpc.call", &ProtocolError{Method: method, Message: "empty method"})
	}
	select {
	case <-c.done:
		return wrapOp("jsonrpc.call", io.ErrClosedPipe)
	default:
	}

	paramBytes, err := json.Marshal(params)
	if err != nil {
		return wrapOp("jsonrpc.call", &ProtocolError{Method: method, Message: "marshal params", Err: err})
	}

	id := atomic.AddUint64(&c.nextID, 1)
	idRaw := json.RawMessage(strconv.AppendUint(nil, id, 10))

	msg := jsonrpcMessage{
		JSONRPC: jsonrpcVersion,
		ID:      idRaw,
		Method:  method,
		Params:  paramBytes,
	}

	respCh := make(chan jsonrpcMessage, 1)
	key := msg.idKey()

	c.mu.Lock()
	c.pending[key] = respCh
	c.mu.Unlock()

	if err := c.writeMessage(msg); err != nil {
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
		close(respCh)
		return wrapOp("jsonrpc.call", err)
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
		close(respCh)
		return wrapOp("jsonrpc.call", ctx.Err())
	case <-c.done:
		return wrapOp("jsonrpc.call", io.ErrClosedPipe)
	case resp, ok := <-respCh:
		if !ok {
			return wrapOp("jsonrpc.call", io.ErrClosedPipe)
		}
		if resp.Error != nil {
			return wrapOp("jsonrpc.call", &ProtocolError{
				Method:  method,
				Code:    resp.Error.Code,
				Message: resp.Error.Message,
				Data:    string(resp.Error.Data),
			})
		}
		if out != nil && len(resp.Result) > 0 && string(resp.Result) != "null" {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return wrapOp("jsonrpc.call", &ProtocolError{Method: method, Message: "unmarshal result", Err: err})
			}
		}
		return nil
	}
}

func (c *conn) notify(ctx context.Context, method string, params any) error {
	if method == "" {
		return wrapOp("jsonrpc.notify", &ProtocolError{Message: "empty method"})
	}
	select {
	case <-c.done:
		return wrapOp("jsonrpc.notify", io.ErrClosedPipe)
	default:
	}

	paramBytes, err := json.Marshal(params)
	if err != nil {
		return wrapOp("jsonrpc.notify", &ProtocolError{Method: method, Message: "marshal params", Err: err})
	}

	msg := jsonrpcMessage{
		JSONRPC: jsonrpcVersion,
		Method:  method,
		Params:  paramBytes,
	}

	done := make(chan error, 1)
	go func() {
		done <- c.writeMessage(msg)
	}()

	select {
	case <-ctx.Done():
		return wrapOp("jsonrpc.notify", ctx.Err())
	case err := <-done:
		return wrapOp("jsonrpc.notify", err)
	}
}

func (c *conn) writeMessage(msg jsonrpcMessage) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if msg.JSONRPC == "" {
		msg.JSONRPC = jsonrpcVersion
	}
	if err := c.enc.Encode(msg); err != nil {
		return &ProtocolError{Message: "encode message", Err: err}
	}
	return nil
}

func (c *conn) readLoop() {
	for {
		var msg jsonrpcMessage
		if err := c.dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				c.shutdown(io.EOF)
				return
			}
			c.shutdown(&ProtocolError{
				Message: "decode message",
				Err:     err,
			})
			return
		}

		if c.maxEventBytes > 0 {
			total := len(msg.Params) + len(msg.Result)
			if msg.Error != nil {
				total += len(msg.Error.Data)
			}
			if total > c.maxEventBytes {
				c.shutdown(&ProtocolError{
					Message: fmt.Sprintf("message exceeded max_event_bytes=%d", c.maxEventBytes),
				})
				return
			}
		}

		switch {
		case msg.Method != "" && msg.hasID():
			c.dispatchRequest(msg)
		case msg.Method != "":
			c.dispatchNotification(msg)
		case msg.hasID():
			c.dispatchResponse(msg)
		default:
			c.pushErr(&ProtocolError{Message: "invalid jsonrpc message"})
		}
	}
}

func (c *conn) dispatchNotification(msg jsonrpcMessage) {
	select {
	case <-c.done:
		return
	case c.notifyCh <- msg:
	default:
		c.pushErr(&ProtocolError{
			Method:  msg.Method,
			Message: "notification buffer full",
		})
	}
}

func (c *conn) dispatchResponse(msg jsonrpcMessage) {
	key := msg.idKey()
	c.mu.Lock()
	respCh, ok := c.pending[key]
	if ok {
		delete(c.pending, key)
	}
	c.mu.Unlock()

	if !ok {
		return
	}
	select {
	case respCh <- msg:
	default:
	}
	close(respCh)
}

func (c *conn) dispatchRequest(msg jsonrpcMessage) {
	c.mu.Lock()
	handler := c.handlers[msg.Method]
	c.mu.Unlock()

	if handler == nil {
		_ = c.writeMessage(jsonrpcMessage{
			JSONRPC: jsonrpcVersion,
			ID:      msg.ID,
			Error: &jsonrpcError{
				Code:    -32601,
				Message: "method not found",
			},
		})
		return
	}

	go func() {
		result, err := handler(context.Background(), msg.Params)
		if err != nil {
			_ = c.writeMessage(jsonrpcMessage{
				JSONRPC: jsonrpcVersion,
				ID:      msg.ID,
				Error: &jsonrpcError{
					Code:    -32000,
					Message: err.Error(),
				},
			})
			return
		}

		resultBytes, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			_ = c.writeMessage(jsonrpcMessage{
				JSONRPC: jsonrpcVersion,
				ID:      msg.ID,
				Error: &jsonrpcError{
					Code:    -32603,
					Message: "internal error",
				},
			})
			return
		}

		_ = c.writeMessage(jsonrpcMessage{
			JSONRPC: jsonrpcVersion,
			ID:      msg.ID,
			Result:  resultBytes,
		})
	}()
}

func (c *conn) close() {
	c.shutdown(io.EOF)
}

func (c *conn) shutdown(cause error) {
	c.closeOnce.Do(func() {
		if cause != nil && !errors.Is(cause, io.EOF) {
			c.pushErr(cause)
		}

		c.mu.Lock()
		pending := c.pending
		c.pending = make(map[string]chan jsonrpcMessage)
		c.mu.Unlock()

		for _, ch := range pending {
			select {
			case ch <- jsonrpcMessage{
				Error: &jsonrpcError{
					Code:    -32001,
					Message: "connection closed",
				},
			}:
			default:
			}
			close(ch)
		}

		close(c.done)
		_ = c.stream.Close()
		close(c.notifyCh)
		close(c.errCh)
	})
}

func (c *conn) pushErr(err error) {
	if err == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	select {
	case c.errCh <- err:
	default:
	}
}

func bytesTrimSpace(raw []byte) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}
