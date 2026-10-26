// Package observability provides structured JSON logging for the AI Loop
// Project. It writes simultaneously to stdout (human-readable, colored)
// and to a rotating JSON file under data/logs/, masking secrets such as
// API keys per guardrail G2.1. Log rotation obeys guardrail G3.3 (max 5
// backups, active file never deleted).
package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/SEU_USUARIO/ai-loop-project/pkg/config"
)

// redactedPlaceholder replaces any detected secret in log output.
// Guardrail G2.1: keys must NEVER appear in logs.
const redactedPlaceholder = "***REDACTED***"

// secretPatterns captures common secret shapes that MUST be masked
// before reaching any log sink. Order does not matter; each pattern is
// applied independently so overlapping matches are still caught.
var secretPatterns = []*regexp.Regexp{
	// OpenAI-style keys: sk-... (both legacy and project keys).
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
	// Bearer tokens in Authorization headers (case-insensitive "bearer").
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-+/=]{8,}`),
	// Generic labeled secrets: api_key=, token=, secret=, password=
	// followed by a long opaque value (with optional surrounding quotes).
	regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password)\s*[:=]\s*["']?[A-Za-z0-9._\-+/=]{16,}["']?`),
}

// Logger is the project's structured logger. It fans out every event to
// stdout and to a rotating JSON file, applying secret masking at the
// writer level so no event leaks raw credentials regardless of the
// caller. Build one per process via New.
type Logger struct {
	root     zerolog.Logger
	fileSink *lumberjack.Logger
	closed   bool
	mu       sync.Mutex
}

// New builds a Logger configured from cfg. It creates the log directory
// if needed, installs the logger as zerolog's global logger (so callers
// may use `log.Info().Msg(...)` anywhere), and returns the Logger for
// explicit use. Returns an error only if the log directory cannot be
// prepared — invalid level surfaces as an error too.
func New(cfg *config.Config) (*Logger, error) {
	if cfg == nil {
		return nil, errors.New("observability: config must not be nil")
	}

	logDir := filepath.Dir(cfg.Logging.File)
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		return nil, fmt.Errorf("observability: create log directory %q: %w", logDir, err)
	}

	level, err := parseLevel(cfg.Logging.Level)
	if err != nil {
		return nil, fmt.Errorf("observability: %w", err)
	}
	zerolog.SetGlobalLevel(level)

	// File sink: JSON, rotating per G3.3 (MaxSizeMB, MaxBackups from cfg).
	fileSink := &lumberjack.Logger{
		Filename:   cfg.Logging.File,
		MaxSize:    cfg.Logging.MaxSizeMB,
		MaxBackups: cfg.Logging.MaxBackups,
		Compress:   false, // keep plaintext rotated files for easy grep
	}

	// stdout sink: human-friendly, colored console writer.
	console := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: "15:04:05"}

	// Wrap BOTH sinks with the masking writer so secrets are redacted
	// before reaching either destination — enforces G2.1 globally.
	maskedConsole := &maskingWriter{w: &console}
	maskedFile := &maskingWriter{w: fileSink}

	multi := zerolog.MultiLevelWriter(maskedConsole, maskedFile)
	root := zerolog.New(multi).With().Timestamp().Logger()

	// Install globally so `log.Info()` and friends work from any package
	// without having to thread the Logger through every function.
	log.Logger = root

	return &Logger{
		root:     root,
		fileSink: fileSink,
	}, nil
}

// Close releases resources held by the file sink. Safe to call multiple
// times; subsequent calls are no-ops and return nil.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed || l.fileSink == nil {
		return nil
	}
	l.closed = true
	if err := l.fileSink.Close(); err != nil {
		return fmt.Errorf("observability: close log file: %w", err)
	}
	return nil
}

// Root returns the underlying zerolog.Logger for advanced usage
// (e.g., attaching contextual fields via `With()`). Most callers should
// use the global `log.*` functions instead.
func (l *Logger) Root() zerolog.Logger { return l.root }

// WithContext returns a logger carrying a "trace_id" field extracted
// from ctx when present. Use inside request-scoped code to correlate
// log lines across components. A nil ctx or a ctx without trace ID
// returns the root logger unchanged.
func (l *Logger) WithContext(ctx context.Context) zerolog.Logger {
	if ctx == nil {
		return l.root
	}
	if id, ok := ctx.Value(traceIDKey).(string); ok && id != "" {
		return l.root.With().Str("trace_id", id).Logger()
	}
	return l.root
}

// WithTraceID returns a context carrying the given trace ID, for use
// with WithContext downstream.
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceIDKey, id)
}

// contextKey is an unexported type for context keys, preventing
// collisions with keys defined in other packages.
type contextKey string

const traceIDKey contextKey = "trace_id"

// maskingWriter is an io.Writer that applies secretPatterns to every
// byte slice before delegating to the wrapped writer. This enforces
// guardrail G2.1 at the lowest possible layer, making secret leaks
// impossible regardless of how callers build their log events.
type maskingWriter struct {
	w io.Writer
}

func (m *maskingWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	s := string(p)
	for _, re := range secretPatterns {
		if re.MatchString(s) {
			s = re.ReplaceAllString(s, redactedPlaceholder)
		}
	}

	n, err := m.w.Write([]byte(s))
	// Report the original length so callers' bookkeeping stays correct
	// (io.Writer contract: return original len unless the error is real).
	if err != nil {
		return n, err
	}
	return len(p), nil
}

// parseLevel maps the YAML level string (case-insensitive) to a
// zerolog.Level. Returns an error for unknown values — the project
// does not silently fall back to a default level.
func parseLevel(s string) (zerolog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return zerolog.TraceLevel, nil
	case "debug":
		return zerolog.DebugLevel, nil
	case "info", "":
		return zerolog.InfoLevel, nil
	case "warn", "warning":
		return zerolog.WarnLevel, nil
	case "error":
		return zerolog.ErrorLevel, nil
	case "fatal":
		return zerolog.FatalLevel, nil
	case "panic":
		return zerolog.PanicLevel, nil
	case "disabled":
		return zerolog.Disabled, nil
	default:
		return zerolog.Disabled, fmt.Errorf("invalid log level %q", s)
	}
}
