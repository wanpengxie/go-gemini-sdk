package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

// Client is a persistent ACP session client backed by a Gemini CLI subprocess.
type Client struct {
	opts   options
	runner processRunner

	mu         sync.RWMutex
	conn       *conn
	process    *processHandle
	stderrRing *stderrRing
	stderrDone <-chan struct{}

	sessionID string
	binary    string
	connected bool
	closing   bool
	closed    bool

	processDone chan struct{}
	processErr  error

	eventsCh chan SessionEvent
	errsCh   chan error

	lastErrMu sync.RWMutex
	lastErr   error

	pumpWG sync.WaitGroup

	closeOutputOnce sync.Once
}

// NewClient creates a client with functional options applied.
func NewClient(opts ...Option) *Client {
	applied := applyOptions(opts)
	runner := applied.runner
	if runner == nil {
		runner = &realRunner{}
	}

	return &Client{
		opts:        applied,
		runner:      runner,
		eventsCh:    make(chan SessionEvent, applied.eventBuffer),
		errsCh:      make(chan error, 16),
		processDone: nil,
	}
}

// Connect starts Gemini CLI and performs ACP initialize + session/new handshake.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.connected {
		c.mu.Unlock()
		return nil
	}
	if c.closed {
		c.mu.Unlock()
		return wrapOp("client.connect", io.ErrClosedPipe)
	}
	c.mu.Unlock()

	startupCtx, startupCancel := withTimeoutIfNeeded(ctx, c.opts.startupTimeout)
	defer startupCancel()

	binary, err := findGemini(startupCtx, c.opts.binaryPath)
	if err != nil {
		return wrapOp("client.connect", err)
	}

	handle, err := c.runner.Start(startupCtx, binary, buildCLIArgs(c.opts), mergeEnv(c.opts.env), c.opts.workDir)
	if err != nil {
		return wrapOp("client.connect", err)
	}

	ring := newStderrRing(c.opts.stderrBufferBytes)
	stderrDone := startStderrDrain(handle.Stderr, ring)

	stream := &stdioStream{reader: handle.Stdout, writer: handle.Stdin}
	rpcConn := newConn(stream, c.opts.maxEventBytes)
	rpcConn.registerHandler(methodRequestPermission, c.handlePermissionRequest)

	if err := c.callInitialize(startupCtx, rpcConn); err != nil {
		c.cleanupFailedConnect(handle, rpcConn, stderrDone)
		return wrapOp("client.connect", err)
	}

	sessionID, err := c.callSessionNew(startupCtx, rpcConn)
	if err != nil {
		c.cleanupFailedConnect(handle, rpcConn, stderrDone)
		return wrapOp("client.connect", err)
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		c.cleanupFailedConnect(handle, rpcConn, stderrDone)
		return wrapOp("client.connect", io.ErrClosedPipe)
	}
	c.conn = rpcConn
	c.process = handle
	c.stderrRing = ring
	c.stderrDone = stderrDone
	c.binary = binary
	c.sessionID = sessionID
	c.connected = true
	c.closing = false
	c.processErr = nil
	c.processDone = make(chan struct{})
	c.mu.Unlock()

	c.pumpWG.Add(3)
	go c.pumpNotifications(rpcConn)
	go c.pumpConnErrors(rpcConn)
	go c.waitProcess()

	go c.closeOutputsWhenStopped()

	return nil
}

// Send issues session/prompt request for the active session.
func (c *Client) Send(ctx context.Context, prompt string) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return wrapOp("client.send", &ProtocolError{Method: methodSessionPrompt, Message: "empty prompt"})
	}

	rpcConn, sessionID, ok := c.snapshotActiveSession()
	if !ok {
		return wrapOp("client.send", io.ErrClosedPipe)
	}

	requestCtx, cancel := withTimeoutIfNeeded(ctx, c.opts.requestTimeout)
	defer cancel()

	var result sessionPromptResult
	err := rpcConn.call(requestCtx, methodSessionPrompt, sessionPromptParams{
		SessionID: sessionID,
		Prompt:    prompt,
	}, &result)
	if err != nil {
		return wrapOp("client.send", err)
	}
	return nil
}

// Receive returns the stream of normalized session/update events.
func (c *Client) Receive() <-chan SessionEvent {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eventsCh
}

// ReceiveWithErrors returns both event stream and asynchronous error stream.
func (c *Client) ReceiveWithErrors() (<-chan SessionEvent, <-chan error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eventsCh, c.errsCh
}

// Interrupt sends session/interrupt notification to Gemini CLI.
func (c *Client) Interrupt(ctx context.Context) error {
	rpcConn, sessionID, ok := c.snapshotActiveSession()
	if !ok {
		return nil
	}

	requestCtx, cancel := withTimeoutIfNeeded(ctx, c.opts.requestTimeout)
	defer cancel()

	err := rpcConn.notify(requestCtx, methodSessionInterrupt, sessionInterruptParams{SessionID: sessionID})
	if err != nil {
		if errors.Is(err, io.ErrClosedPipe) {
			return nil
		}
		return wrapOp("client.interrupt", err)
	}
	return nil
}

// Close gracefully shuts down the client with default close timeout.
func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), c.opts.closeTimeout)
	defer cancel()
	return c.CloseContext(ctx)
}

// CloseContext performs two-phase shutdown: interrupt -> stdin close -> wait -> kill group on timeout.
func (c *Client) CloseContext(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}

	if !c.connected {
		c.closed = true
		c.mu.Unlock()
		c.closeOutputOnce.Do(func() {
			close(c.eventsCh)
			close(c.errsCh)
		})
		return nil
	}

	c.closed = true
	c.closing = true
	proc := c.process
	rpcConn := c.conn
	processDone := c.processDone
	closeTimeout := c.opts.closeTimeout
	c.mu.Unlock()

	closeCtx, cancel := withTimeoutIfNeeded(ctx, closeTimeout)
	defer cancel()

	_ = c.Interrupt(closeCtx)
	if proc != nil && proc.Stdin != nil {
		_ = proc.Stdin.Close()
	}

	waitTimedOut := false
	if processDone != nil {
		select {
		case <-processDone:
		case <-closeCtx.Done():
			waitTimedOut = true
		}
	}

	if waitTimedOut && proc != nil {
		if proc.KillGroup != nil {
			_ = proc.KillGroup()
		} else if proc.Kill != nil {
			_ = proc.Kill()
		}
		if processDone != nil {
			select {
			case <-processDone:
			case <-time.After(500 * time.Millisecond):
			}
		}
	}

	if rpcConn != nil {
		rpcConn.close()
	}

	if waitTimedOut {
		err := wrapProcessError("close_timeout", closeCtx.Err(), c.stderrTail())
		c.recordErr(err)
		c.emitReceiveError(err)
		return wrapOp("client.close", err)
	}

	if err := c.getProcessErr(); err != nil {
		return wrapOp("client.close", wrapProcessError("wait", err, c.stderrTail()))
	}

	return nil
}

// SessionID returns active ACP session id after successful Connect.
func (c *Client) SessionID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessionID
}

// Err returns the latest asynchronous client error.
func (c *Client) Err() error {
	c.lastErrMu.RLock()
	defer c.lastErrMu.RUnlock()
	return c.lastErr
}

func (c *Client) callInitialize(ctx context.Context, rpcConn *conn) error {
	requestCtx, cancel := withTimeoutIfNeeded(ctx, c.opts.requestTimeout)
	defer cancel()

	params := initializeParams{
		ProtocolVersion: "1.0",
		ClientInfo: map[string]any{
			"name":    "go-gemini-sdk",
			"version": "0.1.0",
		},
		Capabilities: map[string]any{},
	}

	var result initializeResult
	if err := rpcConn.call(requestCtx, methodInitialize, params, &result); err != nil {
		return err
	}
	return nil
}

func (c *Client) callSessionNew(ctx context.Context, rpcConn *conn) (string, error) {
	requestCtx, cancel := withTimeoutIfNeeded(ctx, c.opts.requestTimeout)
	defer cancel()

	params := sessionNewParams{
		Model:   c.opts.model,
		WorkDir: c.opts.workDir,
	}

	var result sessionNewResult
	if err := rpcConn.call(requestCtx, methodSessionNew, params, &result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.SessionID) == "" {
		return "", &ProtocolError{Method: methodSessionNew, Message: "empty session id"}
	}
	return result.SessionID, nil
}

func (c *Client) handlePermissionRequest(ctx context.Context, raw json.RawMessage) (any, error) {
	var params requestPermissionParams
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, &ProtocolError{Method: methodRequestPermission, Message: "invalid params", Err: err}
		}
	}

	req := PermissionRequest{
		SessionID: params.SessionID,
		ToolName:  params.ToolName,
		Reason:    params.Reason,
		Args:      params.Args,
	}

	if c.opts.canUseTool == nil {
		return requestPermissionResult{Allow: true, Reason: "no callback configured"}, nil
	}

	cbCtx, cancel := withTimeoutIfNeeded(ctx, c.opts.requestTimeout)
	defer cancel()

	res, err := c.opts.canUseTool(cbCtx, req)
	if err != nil {
		return nil, err
	}

	return requestPermissionResult{Allow: res.Allow, Reason: res.Reason}, nil
}

func (c *Client) pumpNotifications(rpcConn *conn) {
	defer c.pumpWG.Done()

	for msg := range rpcConn.Notifications() {
		if msg.Method != methodSessionUpdate {
			continue
		}

		var update sessionUpdateParams
		if len(msg.Params) > 0 && string(msg.Params) != "null" {
			if err := json.Unmarshal(msg.Params, &update); err != nil {
				c.emitReceiveError(&ProtocolError{Method: methodSessionUpdate, Message: "invalid session update", Err: err})
				continue
			}
		}

		c.emitEvent(SessionEvent{
			Type:       normalizeEventType(update.Type),
			SessionID:  update.SessionID,
			TurnID:     update.TurnID,
			Role:       update.Role,
			Text:       update.Text,
			ToolName:   update.ToolName,
			ToolCallID: update.ToolCallID,
			Done:       update.Done,
			Error:      update.Error,
			Data:       update.Data,
		})
	}
}

func (c *Client) pumpConnErrors(rpcConn *conn) {
	defer c.pumpWG.Done()

	for err := range rpcConn.Errors() {
		c.emitReceiveError(wrapOp("client.read_loop", err))
	}
}

func (c *Client) waitProcess() {
	defer c.pumpWG.Done()

	c.mu.RLock()
	proc := c.process
	stderrDone := c.stderrDone
	rpcConn := c.conn
	processDone := c.processDone
	c.mu.RUnlock()

	var waitErr error
	if proc != nil && proc.Wait != nil {
		waitErr = proc.Wait()
	}
	if stderrDone != nil {
		<-stderrDone
	}

	c.setProcessErr(waitErr)
	if processDone != nil {
		close(processDone)
	}

	if waitErr != nil && !c.isClosing() {
		c.emitReceiveError(wrapProcessError("wait", waitErr, c.stderrTail()))
	}

	if rpcConn != nil {
		rpcConn.close()
	}
}

func (c *Client) closeOutputsWhenStopped() {
	c.pumpWG.Wait()
	c.closeOutputOnce.Do(func() {
		close(c.eventsCh)
		close(c.errsCh)
	})
}

func (c *Client) snapshotActiveSession() (*conn, string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.connected || c.conn == nil || c.sessionID == "" {
		return nil, "", false
	}
	return c.conn, c.sessionID, true
}

func (c *Client) emitEvent(event SessionEvent) {
	defer func() {
		_ = recover()
	}()
	select {
	case c.eventsCh <- event:
	default:
		c.emitReceiveError(&ProtocolError{Method: methodSessionUpdate, Message: "event buffer full"})
	}
}

func (c *Client) emitReceiveError(err error) {
	if err == nil {
		return
	}
	c.recordErr(err)
	defer func() {
		_ = recover()
	}()
	select {
	case c.errsCh <- err:
	default:
	}
}

func (c *Client) recordErr(err error) {
	if err == nil {
		return
	}
	c.lastErrMu.Lock()
	c.lastErr = err
	c.lastErrMu.Unlock()
}

func (c *Client) setProcessErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.processErr = err
	if c.connected {
		c.connected = false
	}
}

func (c *Client) getProcessErr() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.processErr
}

func (c *Client) isClosing() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.closing
}

func (c *Client) stderrTail() string {
	c.mu.RLock()
	ring := c.stderrRing
	c.mu.RUnlock()
	if ring == nil {
		return ""
	}
	return sanitizeStderrTail(ring.String())
}

func (c *Client) cleanupFailedConnect(handle *processHandle, rpcConn *conn, stderrDone <-chan struct{}) {
	if rpcConn != nil {
		rpcConn.close()
	}
	if handle != nil && handle.Stdin != nil {
		_ = handle.Stdin.Close()
	}
	if handle != nil {
		if handle.KillGroup != nil {
			_ = handle.KillGroup()
		} else if handle.Kill != nil {
			_ = handle.Kill()
		}
		if handle.Wait != nil {
			_ = handle.Wait()
		}
	}
	if stderrDone != nil {
		<-stderrDone
	}
}

func normalizeEventType(t string) EventType {
	switch strings.TrimSpace(t) {
	case string(EventTypeMessage):
		return EventTypeMessage
	case string(EventTypeMessageChunk):
		return EventTypeMessageChunk
	case string(EventTypeToolCall):
		return EventTypeToolCall
	case string(EventTypeToolCallUpdate):
		return EventTypeToolCallUpdate
	case string(EventTypeCompleted):
		return EventTypeCompleted
	case string(EventTypeError):
		return EventTypeError
	default:
		return EventTypeUnknown
	}
}

type stdioStream struct {
	reader io.ReadCloser
	writer io.WriteCloser
}

func (s *stdioStream) Read(p []byte) (int, error) {
	if s.reader == nil {
		return 0, io.EOF
	}
	return s.reader.Read(p)
}

func (s *stdioStream) Write(p []byte) (int, error) {
	if s.writer == nil {
		return 0, io.ErrClosedPipe
	}
	return s.writer.Write(p)
}

func (s *stdioStream) Close() error {
	var errs []error
	if s.writer != nil {
		err := s.writer.Close()
		if err != nil && !errors.Is(err, io.ErrClosedPipe) {
			errs = append(errs, err)
		}
	}
	if s.reader != nil {
		err := s.reader.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
