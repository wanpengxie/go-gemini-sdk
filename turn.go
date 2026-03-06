package gemini

import (
	"strings"
	"sync"
)

// TurnHandle represents one in-flight response turn.
type TurnHandle struct {
	messagesCh chan Message
	errsCh     chan error
	done       chan struct{}

	onClose func()

	mu          sync.Mutex
	sessionID   string
	turnID      string
	stopReason  string
	promptAcked bool
	terminal    bool
	turnErr     string

	closeOnce sync.Once
	closed    bool
}

func newTurnHandle(sessionID string, buffer int, onClose func()) *TurnHandle {
	if buffer <= 0 {
		buffer = 1
	}
	return &TurnHandle{
		messagesCh: make(chan Message, buffer),
		errsCh:     make(chan error, 1),
		done:       make(chan struct{}),
		onClose:    onClose,
		sessionID:  sessionID,
	}
}

// Messages returns the typed message stream for this turn.
func (t *TurnHandle) Messages() <-chan Message {
	return t.messagesCh
}

// Errors returns the terminal error channel for this turn.
func (t *TurnHandle) Errors() <-chan error {
	return t.errsCh
}

func (t *TurnHandle) Done() <-chan struct{} {
	return t.done
}

func (t *TurnHandle) handleEvent(ev sessionEvent) {
	var msg Message
	var result *ResultMessage

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	if ev.SessionID != "" {
		t.sessionID = ev.SessionID
	}
	if ev.TurnID != "" {
		t.turnID = ev.TurnID
	}
	msg = messageFromEvent(ev)
	if isTerminalEvent(ev) {
		t.terminal = true
		if errText := strings.TrimSpace(ev.Error); errText != "" {
			t.turnErr = errText
		}
		result = t.tryCloseLocked()
	}
	t.mu.Unlock()

	if msg != nil {
		t.messagesCh <- msg
	}
	if result != nil {
		t.messagesCh <- result
		t.close(nil)
	}
}

func (t *TurnHandle) handlePromptResult(result sessionPromptResult) {
	var final *ResultMessage

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.promptAcked = true
	if id := strings.TrimSpace(result.TurnID); id != "" {
		t.turnID = id
	}
	if reason := strings.TrimSpace(result.StopReason); reason != "" {
		t.stopReason = reason
		t.terminal = true
	}
	final = t.tryCloseLocked()
	t.mu.Unlock()

	if final != nil {
		t.messagesCh <- final
		t.close(nil)
	}
}

func (t *TurnHandle) fail(err error) {
	t.close(err)
}

func (t *TurnHandle) tryCloseLocked() *ResultMessage {
	if t.closed || !t.promptAcked || !t.terminal {
		return nil
	}
	t.closed = true
	return &ResultMessage{
		SessionID:  t.sessionID,
		TurnID:     t.turnID,
		StopReason: t.stopReason,
		IsError:    strings.TrimSpace(t.turnErr) != "",
		Error:      t.turnErr,
	}
}

func (t *TurnHandle) close(err error) {
	t.closeOnce.Do(func() {
		if err != nil {
			t.errsCh <- err
		}
		close(t.done)
		close(t.messagesCh)
		close(t.errsCh)
		if t.onClose != nil {
			t.onClose()
		}
	})
}

func isTerminalEvent(ev sessionEvent) bool {
	return ev.Done || ev.Type == eventTypeCompleted || ev.Type == eventTypeError
}
