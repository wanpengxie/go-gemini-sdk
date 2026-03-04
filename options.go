package gemini

import (
	"context"
	"time"
)

const (
	defaultStartupTimeout    = 30 * time.Second
	defaultRequestTimeout    = 60 * time.Second
	defaultCloseTimeout      = 10 * time.Second
	defaultMaxEventBytes     = 2 << 20 // 2 MiB
	defaultStderrBufferBytes = 8 << 10 // 8 KiB
	defaultEventBuffer       = 128
)

// Option configures Client.
type Option func(*options)

type options struct {
	binaryPath        string
	args              []string
	env               []string
	workDir           string
	model             string
	sandbox           string
	startupTimeout    time.Duration
	requestTimeout    time.Duration
	closeTimeout      time.Duration
	maxEventBytes     int
	stderrBufferBytes int
	eventBuffer       int
	runner            processRunner
	canUseTool        CanUseToolFunc
}

func defaultOptions() options {
	return options{
		args:              []string{"--experimental-acp"},
		startupTimeout:    defaultStartupTimeout,
		requestTimeout:    defaultRequestTimeout,
		closeTimeout:      defaultCloseTimeout,
		maxEventBytes:     defaultMaxEventBytes,
		stderrBufferBytes: defaultStderrBufferBytes,
		eventBuffer:       defaultEventBuffer,
	}
}

// WithBinaryPath sets the gemini CLI executable path.
func WithBinaryPath(path string) Option {
	return func(o *options) {
		o.binaryPath = path
	}
}

// WithArgs appends CLI arguments.
func WithArgs(args ...string) Option {
	return func(o *options) {
		o.args = append([]string(nil), args...)
	}
}

// WithEnv appends process environment variables.
func WithEnv(env ...string) Option {
	return func(o *options) {
		o.env = append([]string(nil), env...)
	}
}

// WithWorkDir sets process working directory.
func WithWorkDir(dir string) Option {
	return func(o *options) {
		o.workDir = dir
	}
}

// WithModel sets the model to be used by Gemini CLI.
func WithModel(model string) Option {
	return func(o *options) {
		o.model = model
	}
}

// WithSandbox sets sandbox mode placeholder for future tickets.
func WithSandbox(mode string) Option {
	return func(o *options) {
		o.sandbox = mode
	}
}

// WithStartupTimeout sets process startup timeout.
func WithStartupTimeout(timeout time.Duration) Option {
	return func(o *options) {
		if timeout > 0 {
			o.startupTimeout = timeout
		}
	}
}

// WithRequestTimeout sets per RPC request timeout.
func WithRequestTimeout(timeout time.Duration) Option {
	return func(o *options) {
		if timeout > 0 {
			o.requestTimeout = timeout
		}
	}
}

// WithCloseTimeout sets graceful close timeout.
func WithCloseTimeout(timeout time.Duration) Option {
	return func(o *options) {
		if timeout > 0 {
			o.closeTimeout = timeout
		}
	}
}

// WithMaxEventBytes sets max raw bytes for params/result in one message.
func WithMaxEventBytes(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.maxEventBytes = n
		}
	}
}

// WithStderrBufferBytes sets ring buffer bytes for stderr tail.
func WithStderrBufferBytes(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.stderrBufferBytes = n
		}
	}
}

// WithEventBuffer sets SessionEvent channel buffer size.
func WithEventBuffer(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.eventBuffer = n
		}
	}
}

// WithRunner injects a custom process runner, mainly used by tests.
func WithRunner(r processRunner) Option {
	return func(o *options) {
		o.runner = r
	}
}

// WithCanUseTool registers permission callback for request_permission requests.
func WithCanUseTool(fn CanUseToolFunc) Option {
	return func(o *options) {
		o.canUseTool = fn
	}
}

func applyOptions(opts []Option) options {
	o := defaultOptions()
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

func withTimeoutIfNeeded(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}
