package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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

	sessionID  string
	binary     string
	connected  bool
	closing    bool
	closed     bool
	activeTurn *TurnHandle

	processDone   chan struct{}
	processErr    error
	launchCleanup func()

	lastErrMu sync.RWMutex
	lastErr   error

	pumpWG sync.WaitGroup
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

	preparedOpts, launchCleanup, err := prepareLaunchOptions(c.opts)
	if err != nil {
		return wrapOp("client.connect", err)
	}
	defer func() {
		if launchCleanup != nil {
			launchCleanup()
		}
	}()

	binary, err := findGemini(startupCtx, preparedOpts.binaryPath)
	if err != nil {
		return wrapOp("client.connect", err)
	}

	handle, err := c.runner.Start(startupCtx, binary, buildCLIArgs(preparedOpts), mergeEnv(preparedOpts.env), preparedOpts.workDir)
	if err != nil {
		return wrapOp("client.connect", err)
	}

	ring := newStderrRing(c.opts.stderrBufferBytes)
	stderrDone := startStderrDrain(handle.Stderr, ring)

	stream := &stdioStream{reader: handle.Stdout, writer: handle.Stdin}
	rpcConn := newConn(stream, c.opts.maxEventBytes)
	rpcConn.registerHandler(methodRequestPermission, c.handlePermissionRequest)
	rpcConn.registerHandler(methodSessionRequestPermission, c.handlePermissionRequest)

	if err := c.callInitialize(startupCtx, rpcConn); err != nil {
		err = wrapProcessError("initialize", err, sanitizeStderrTail(ring.String()))
		c.cleanupFailedConnect(handle, rpcConn, stderrDone)
		return wrapOp("client.connect", err)
	}

	sessionID, err := c.callSessionNew(startupCtx, rpcConn)
	if err != nil {
		err = wrapProcessError("session_new", err, sanitizeStderrTail(ring.String()))
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
	c.launchCleanup = launchCleanup
	c.mu.Unlock()
	launchCleanup = nil

	c.pumpWG.Add(3)
	go c.pumpNotifications(rpcConn)
	go c.pumpConnErrors(rpcConn)
	go c.waitProcess()

	return nil
}

// Query submits one prompt turn and returns a dedicated turn handle.
func (c *Client) Query(ctx context.Context, prompt string) (*TurnHandle, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, wrapOp("client.query", &ProtocolError{Method: methodSessionPrompt, Message: "empty prompt"})
	}
	if err := ctx.Err(); err != nil {
		return nil, wrapOp("client.query", err)
	}

	rpcConn, sessionID, turn, err := c.beginTurn()
	if err != nil {
		return nil, err
	}

	call, err := rpcConn.beginCall(methodSessionPrompt, sessionPromptParams{
		SessionID: sessionID,
		Prompt: []promptContentBlock{
			{Type: "text", Text: prompt},
		},
	})
	if err != nil {
		c.releaseTurn(turn)
		if errors.Is(err, io.ErrClosedPipe) {
			return nil, wrapOp("client.query", c.connectionInactiveError("query"))
		}
		return nil, wrapOp("client.query", err)
	}

	waitCtx, cancel := withTimeoutIfNeeded(context.Background(), c.opts.requestTimeout)
	go c.awaitPromptResult(waitCtx, cancel, call, turn)
	return turn, nil
}

// Interrupt sends session/interrupt notification to Gemini CLI.
func (c *Client) Interrupt(ctx context.Context) error {
	rpcConn, sessionID, ok := c.snapshotActiveSession()
	if !ok {
		return wrapOp("client.interrupt", c.connectionInactiveError("interrupt"))
	}

	requestCtx, cancel := withTimeoutIfNeeded(ctx, c.opts.requestTimeout)
	defer cancel()

	err := rpcConn.notify(requestCtx, methodSessionInterrupt, sessionInterruptParams{
		SessionID: sessionID,
	})
	if err != nil {
		if errors.Is(err, io.ErrClosedPipe) {
			return wrapOp("client.interrupt", c.connectionInactiveError("interrupt"))
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
		return nil
	}

	c.closed = true
	c.closing = true
	proc := c.process
	rpcConn := c.conn
	processDone := c.processDone
	closeTimeout := c.opts.closeTimeout
	activeTurn := c.activeTurn
	c.mu.Unlock()

	if activeTurn != nil {
		activeTurn.fail(wrapOp("client.close", io.ErrClosedPipe))
	}

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
		c.routeTurnError(err)
		return wrapOp("client.close", err)
	}

	if err := c.getProcessErr(); err != nil {
		return wrapOp("client.close", err)
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
		ProtocolVersion: 1,
		ClientInfo: map[string]any{
			"name":    "go-gemini-sdk",
			"version": "0.1.0",
		},
		ClientCapabilities: map[string]any{
			"fs": map[string]any{
				"readTextFile":  false,
				"writeTextFile": false,
			},
			"terminal": false,
		},
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

	cwd := strings.TrimSpace(c.opts.workDir)
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}
	if cwd == "" {
		cwd = "/"
	}
	if cwd != "" {
		if abs, err := filepath.Abs(cwd); err == nil {
			cwd = abs
		}
	}

	params := sessionNewParams{
		Cwd:        cwd,
		MCPServers: []map[string]any{},
	}

	var result sessionNewResult
	if err := rpcConn.call(requestCtx, methodSessionNew, params, &result); err != nil {
		return "", err
	}
	sessionID := result.EffectiveSessionID()
	if strings.TrimSpace(sessionID) == "" {
		return "", &ProtocolError{Method: methodSessionNew, Message: "empty session id"}
	}
	return sessionID, nil
}

func (c *Client) handlePermissionRequest(ctx context.Context, raw json.RawMessage) (any, error) {
	var params requestPermissionParams
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, &ProtocolError{Method: methodRequestPermission, Message: "invalid params", Err: err}
		}
	}

	toolCall := normalizeToolCallInfo(params)
	options := params.Options

	selectedOptionID := ""
	if c.opts.canUseTool != nil {
		cbCtx, cancel := withTimeoutIfNeeded(ctx, c.opts.requestTimeout)
		defer cancel()

		var err error
		selectedOptionID, err = c.opts.canUseTool(cbCtx, toolCall, options)
		if err != nil {
			return nil, err
		}
	}

	if strings.TrimSpace(selectedOptionID) == "" {
		selectedOptionID = pickSafeFallbackOption(options)
	}
	if selectedOptionID == "" {
		return nil, &ProtocolError{Method: methodRequestPermission, Message: "missing permission options"}
	}
	if !hasPermissionOption(options, selectedOptionID) {
		return nil, &ProtocolError{Method: methodRequestPermission, Message: "selected option id not found"}
	}

	return requestPermissionResult{
		Outcome: &requestPermissionOutcome{
			Outcome:  "selected",
			OptionID: selectedOptionID,
		},
	}, nil
}

func normalizeToolCallInfo(params requestPermissionParams) ToolCallInfo {
	sessionID := strings.TrimSpace(params.SessionIDV2)
	if sessionID == "" {
		sessionID = strings.TrimSpace(params.SessionID)
	}

	toolCall := params.ToolCallV2
	if toolCall == nil {
		toolCall = params.ToolCall
	}

	call := ToolCallInfo{
		SessionID: sessionID,
		ToolName:  strings.TrimSpace(params.ToolName),
		ToolKind:  normalizeToolKind(params.ToolKind, params.ToolName),
		Reason:    params.Reason,
		Args:      params.Args,
	}
	if toolCall == nil {
		return call
	}
	if call.ToolName == "" {
		call.ToolName = strings.TrimSpace(toolCall.Name)
	}
	if call.ToolName == "" {
		call.ToolName = strings.TrimSpace(toolCall.Title)
	}
	if call.ToolKind == ToolKindUnknown {
		call.ToolKind = normalizeToolKind(toolCall.Kind, toolCall.Name)
	}
	if len(call.Args) == 0 && len(toolCall.Args) > 0 {
		call.Args = toolCall.Args
	}
	return call
}

func normalizeToolKind(kind ToolKind, toolName string) ToolKind {
	if k := strings.ToLower(strings.TrimSpace(string(kind))); k != "" {
		return ToolKind(k)
	}
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case string(ToolKindRead):
		return ToolKindRead
	case string(ToolKindEdit):
		return ToolKindEdit
	case string(ToolKindBash), "shell":
		return ToolKindBash
	default:
		return ToolKindUnknown
	}
}

func pickSafeFallbackOption(options []PermissionOption) string {
	if id := findOptionByPrefix(options, "reject_"); id != "" {
		return id
	}
	if id := findOptionByPrefix(options, "allow_"); id != "" {
		return id
	}
	for _, option := range options {
		if id := option.normalizedID(); id != "" {
			return id
		}
	}
	return ""
}

func findOptionByPrefix(options []PermissionOption, prefix string) string {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix == "" {
		return ""
	}
	for _, option := range options {
		id := option.normalizedID()
		if id == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(id), prefix) {
			return id
		}
	}
	return ""
}

func hasPermissionOption(options []PermissionOption, selectedOptionID string) bool {
	selectedOptionID = strings.TrimSpace(selectedOptionID)
	if selectedOptionID == "" {
		return false
	}
	for _, option := range options {
		if option.normalizedID() == selectedOptionID {
			return true
		}
	}
	return false
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
				c.routeTurnError(&ProtocolError{Method: methodSessionUpdate, Message: "invalid session update", Err: err})
				continue
			}
		}
		sessionID, rawType, text, toolName, toolCallID, done, eventErr, payload := normalizeSessionUpdate(update)
		c.routeTurnEvent(sessionEvent{
			Type:       normalizeEventType(rawType),
			RawType:    rawType,
			SessionID:  sessionID,
			TurnID:     update.TurnID,
			Role:       update.Role,
			Text:       text,
			ToolName:   toolName,
			ToolCallID: toolCallID,
			Done:       done,
			Error:      eventErr,
			Data:       payload,
		})
	}
}

func (c *Client) pumpConnErrors(rpcConn *conn) {
	defer c.pumpWG.Done()

	for err := range rpcConn.Errors() {
		c.routeTurnError(wrapOp("client.read_loop", err))
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

	processErr := wrapProcessError("wait", waitErr, c.stderrTail())
	c.setProcessErr(processErr)
	if processDone != nil {
		close(processDone)
	}

	if waitErr != nil && !c.isClosing() {
		c.routeTurnError(processErr)
	}

	if waitErr != nil && rpcConn != nil {
		rpcConn.close()
	}

	c.cleanupLaunchArtifacts()
}

func (c *Client) beginTurn() (*conn, string, *TurnHandle, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected || c.conn == nil || c.sessionID == "" {
		return nil, "", nil, wrapOp("client.query", c.connectionInactiveError("query"))
	}
	if c.activeTurn != nil {
		return nil, "", nil, wrapOp("client.query", &ProtocolError{
			Method:  methodSessionPrompt,
			Message: "turn already in progress",
		})
	}

	var turn *TurnHandle
	turn = newTurnHandle(c.sessionID, c.opts.eventBuffer, func() {
		c.releaseTurn(turn)
	})
	c.activeTurn = turn
	return c.conn, c.sessionID, turn, nil
}

func (c *Client) releaseTurn(turn *TurnHandle) {
	c.mu.Lock()
	if c.activeTurn == turn {
		c.activeTurn = nil
	}
	c.mu.Unlock()
}

func (c *Client) awaitPromptResult(ctx context.Context, cancel context.CancelFunc, call *pendingCall, turn *TurnHandle) {
	defer cancel()

	var result sessionPromptResult
	err := call.wait(ctx, &result)
	if err != nil {
		if errors.Is(err, io.ErrClosedPipe) {
			c.routeTurnError(wrapOp("client.query", c.connectionInactiveError("query")))
			return
		}
		c.routeTurnError(wrapOp("client.query", err))
		return
	}
	turn.handlePromptResult(result)
}

func (c *Client) snapshotActiveSession() (*conn, string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.connected || c.conn == nil || c.sessionID == "" {
		return nil, "", false
	}
	return c.conn, c.sessionID, true
}

func (c *Client) routeTurnEvent(event sessionEvent) {
	turn := c.currentTurn()
	if turn == nil {
		return
	}
	turn.handleEvent(event)
}

func (c *Client) routeTurnError(err error) {
	if err == nil {
		return
	}
	c.recordErr(err)
	turn := c.currentTurn()
	if turn != nil {
		turn.fail(err)
	}
}

func (c *Client) currentTurn() *TurnHandle {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.activeTurn
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

func (c *Client) cleanupLaunchArtifacts() {
	c.mu.Lock()
	cleanup := c.launchCleanup
	c.launchCleanup = nil
	c.mu.Unlock()

	if cleanup != nil {
		cleanup()
	}
}

func (c *Client) connectionInactiveError(op string) error {
	cause := c.getProcessErr()
	if cause == nil {
		cause = io.ErrClosedPipe
	}
	return &ConnectionInactiveError{
		Op:    op,
		Cause: cause,
	}
}

func normalizeSessionUpdate(update sessionUpdateParams) (sessionID, rawType, text, toolName, toolCallID string, done bool, eventErr string, payload json.RawMessage) {
	if update.Update != nil {
		sessionID = strings.TrimSpace(update.SessionIDV2)
		if sessionID == "" {
			sessionID = strings.TrimSpace(update.SessionID)
		}
		rawType = strings.TrimSpace(update.Update.SessionUpdate)
		toolCallID = strings.TrimSpace(update.Update.ToolCallID)
		toolName = strings.TrimSpace(update.Update.Title)
		text = extractTextFromContent(update.Update.Content)
		eventErr = strings.TrimSpace(update.Error)
		if payloadBytes, err := json.Marshal(update.Update); err == nil {
			payload = payloadBytes
		}
		return
	}

	sessionID = strings.TrimSpace(update.SessionID)
	rawType = strings.TrimSpace(update.Type)
	text = update.Text
	toolName = update.ToolName
	toolCallID = update.ToolCallID
	done = update.Done
	eventErr = update.Error
	payload = update.Data
	return
}

func extractTextFromContent(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	var block struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	}
	if err := json.Unmarshal(raw, &block); err == nil {
		if strings.EqualFold(strings.TrimSpace(block.Type), "text") {
			return block.Text
		}
	}

	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}

	var out strings.Builder
	for _, item := range blocks {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "text") {
			continue
		}
		out.WriteString(item.Text)
	}
	return out.String()
}

func normalizeEventType(t string) eventType {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case string(eventTypeMessage), "agent_message":
		return eventTypeMessage
	case string(eventTypeMessageChunk), "agent_message_chunk", "user_message_chunk":
		return eventTypeMessageChunk
	case string(eventTypeThinking), "thinking_chunk", "thought", "thought_chunk", "agent_thought_chunk", "agent_thinking", "agent_thinking_chunk":
		return eventTypeThinking
	case string(eventTypeToolCall), "toolcall":
		return eventTypeToolCall
	case string(eventTypeToolCallUpdate), "tool_result", "tool_call_result", "tool_result_chunk":
		return eventTypeToolCallUpdate
	case string(eventTypeCompleted), "done", "turn_completed", "complete":
		return eventTypeCompleted
	case string(eventTypeError), "failed":
		return eventTypeError
	default:
		return eventTypeUnknown
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
