package gemini

import (
	"errors"
	"fmt"
)

var (
	// ErrCLINotFound indicates gemini CLI binary could not be discovered.
	ErrCLINotFound = errors.New("gemini cli not found")
	// ErrProcess indicates subprocess related failures.
	ErrProcess = errors.New("gemini process error")
	// ErrProtocol indicates protocol or JSON-RPC failures.
	ErrProtocol = errors.New("gemini protocol error")
	// ErrConnectionInactive indicates RPC call attempted on an inactive connection.
	ErrConnectionInactive = errors.New("gemini connection inactive")
)

// SDKError wraps operation-level context.
type SDKError struct {
	Op  string
	Err error
}

func (e *SDKError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Op == "" {
		return e.Err.Error()
	}
	return fmt.Sprintf("%s: %v", e.Op, e.Err)
}

func (e *SDKError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// CLINotFoundError indicates the binary was not found in any candidate path.
type CLINotFoundError struct {
	Attempts []string
}

func (e *CLINotFoundError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if len(e.Attempts) == 0 {
		return ErrCLINotFound.Error()
	}
	return fmt.Sprintf("%s (tried: %v)", ErrCLINotFound.Error(), e.Attempts)
}

func (e *CLINotFoundError) Is(target error) bool {
	return target == ErrCLINotFound
}

// ProcessError indicates subprocess start/wait/kill failures.
type ProcessError struct {
	Op         string
	ExitCode   int
	StderrTail string
	Err        error
}

func (e *ProcessError) Error() string {
	if e == nil {
		return "<nil>"
	}
	msg := ErrProcess.Error()
	if e.Op != "" {
		msg = fmt.Sprintf("%s (%s)", msg, e.Op)
	}
	if e.ExitCode != 0 {
		msg = fmt.Sprintf("%s exit=%d", msg, e.ExitCode)
	}
	if e.StderrTail != "" {
		msg = fmt.Sprintf("%s stderr=%q", msg, e.StderrTail)
	}
	if e.Err != nil {
		msg = fmt.Sprintf("%s: %v", msg, e.Err)
	}
	return msg
}

func (e *ProcessError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *ProcessError) Is(target error) bool {
	return target == ErrProcess
}

// ProtocolError indicates ACP/JSON-RPC level errors.
type ProtocolError struct {
	Method  string
	Code    int
	Message string
	Data    string
	Err     error
}

func (e *ProtocolError) Error() string {
	if e == nil {
		return "<nil>"
	}
	msg := ErrProtocol.Error()
	if e.Method != "" {
		msg = fmt.Sprintf("%s method=%s", msg, e.Method)
	}
	if e.Code != 0 {
		msg = fmt.Sprintf("%s code=%d", msg, e.Code)
	}
	if e.Message != "" {
		msg = fmt.Sprintf("%s message=%s", msg, e.Message)
	}
	if e.Data != "" {
		msg = fmt.Sprintf("%s data=%s", msg, e.Data)
	}
	if e.Err != nil {
		msg = fmt.Sprintf("%s: %v", msg, e.Err)
	}
	return msg
}

func (e *ProtocolError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *ProtocolError) Is(target error) bool {
	return target == ErrProtocol
}

// ConnectionInactiveError indicates operation failed because the client
// connection is already inactive.
type ConnectionInactiveError struct {
	Op    string
	Cause error
}

func (e *ConnectionInactiveError) Error() string {
	if e == nil {
		return "<nil>"
	}
	msg := ErrConnectionInactive.Error()
	if e.Op != "" {
		msg = fmt.Sprintf("%s (%s)", msg, e.Op)
	}
	if e.Cause != nil {
		msg = fmt.Sprintf("%s: %v", msg, e.Cause)
	}
	return msg
}

func (e *ConnectionInactiveError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *ConnectionInactiveError) Is(target error) bool {
	return target == ErrConnectionInactive
}

func wrapOp(op string, err error) error {
	if err == nil {
		return nil
	}
	return &SDKError{
		Op:  op,
		Err: err,
	}
}
