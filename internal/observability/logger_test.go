package observability

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SEU_USUARIO/ai-loop-project/pkg/config"
)

// newTestConfig returns a minimal Config pointing the log file at a path
// inside tmpDir. Logging level defaults to debug so tests can exercise
// every level.
func newTestConfig(t *testing.T, tmpDir string) *config.Config {
	t.Helper()
	return &config.Config{
		Logging: config.Logging{
			Level:      "debug",
			File:       filepath.Join(tmpDir, "logs", "ai_loop.log"),
			MaxSizeMB:  1,
			MaxBackups: 2,
			JSON:       true,
		},
	}
}

func TestNew_NilConfig(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) must return error")
	}
}

func TestNew_CreatesLogDirectory(t *testing.T) {
	tmp := t.TempDir()
	cfg := newTestConfig(t, tmp)

	logger, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer logger.Close()

	info, err := os.Stat(filepath.Dir(cfg.Logging.File))
	if err != nil {
		t.Fatalf("log directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("log path is not a directory")
	}
}

func TestLogger_WritesStructuredEventToFile(t *testing.T) {
	tmp := t.TempDir()
	cfg := newTestConfig(t, tmp)

	logger, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer logger.Close()

	l := logger.Root()
	l.Info().
		Str("event", "hello").
		Int("pipeline_id", 42).
		Msg("test message")

	content, err := os.ReadFile(cfg.Logging.File)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	out := string(content)

	if !strings.Contains(out, "test message") {
		t.Fatalf("log file missing message: %q", out)
	}
	if !strings.Contains(out, `"event":"hello"`) {
		t.Fatalf("log file missing structured field: %q", out)
	}
	if !strings.Contains(out, `"pipeline_id":42`) {
		t.Fatalf("log file missing int field: %q", out)
	}
}

func TestMaskingWriter_MasksSecrets(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantRedacted bool
		needle       string // substring that must disappear when redacted
	}{
		{
			name:         "openai_project_key",
			input:        `Authorization: Bearer sk-proj-abc123def456ghi789jkl012mno345pqr678`,
			wantRedacted: true,
			needle:       "sk-proj-abc123def456ghi789jkl012mno345pqr678",
		},
		{
			name:         "legacy_sk",
			input:        `key=sk-abcdefghijklmnopqrstuvwxyz0123456789`,
			wantRedacted: true,
			needle:       "sk-abcdefghijklmnopqrstuvwxyz0123456789",
		},
		{
			name:         "bearer_jwt",
			input:        `Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP04sR`,
			wantRedacted: true,
			needle:       "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
		},
		{
			name:         "api_key_field",
			input:        `api_key: abcdefghijklmnopqrstuvwxyz0123456789`,
			wantRedacted: true,
			needle:       "abcdefghijklmnopqrstuvwxyz0123456789",
		},
		{
			name:         "password_field",
			input:        `password="super-secret-value-1234567890"`,
			wantRedacted: true,
			needle:       "super-secret-value-1234567890",
		},
		{
			name:         "safe_message",
			input:        `hello world, no secrets here (cost=0.50)`,
			wantRedacted: false,
			needle:       "no secrets here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			mw := &maskingWriter{w: &buf}

			n, err := mw.Write([]byte(tt.input))
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			if n != len(tt.input) {
				t.Fatalf("Write returned %d, want %d (io.Writer contract)", n, len(tt.input))
			}

			out := buf.String()
			hasRedacted := strings.Contains(out, redactedPlaceholder)
			hasNeedle := strings.Contains(out, tt.needle)

			if tt.wantRedacted {
				if !hasRedacted {
					t.Errorf("expected redaction, got: %q", out)
				}
				if hasNeedle {
					t.Errorf("secret leaked: %q still contains %q", out, tt.needle)
				}
			} else {
				if hasRedacted {
					t.Errorf("unexpected redaction in: %q", out)
				}
				if !hasNeedle {
					t.Errorf("safe content mangled: %q missing %q", out, tt.needle)
				}
			}
		})
	}
}

func TestMaskingWriter_EmptyWrite(t *testing.T) {
	var buf bytes.Buffer
	mw := &maskingWriter{w: &buf}
	n, err := mw.Write(nil)
	if err != nil {
		t.Fatalf("Write(nil): %v", err)
	}
	if n != 0 {
		t.Fatalf("Write(nil) = %d, want 0", n)
	}
	if buf.Len() != 0 {
		t.Fatalf("buffer not empty after nil write")
	}
}

func TestLogger_LevelFiltering(t *testing.T) {
	tmp := t.TempDir()
	cfg := newTestConfig(t, tmp)
	cfg.Logging.Level = "warn"

	logger, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer logger.Close()

	l := logger.Root()
	// These should NOT appear in the file.
	l.Debug().Msg("debug filtered out")
	l.Info().Msg("info filtered out")
	// These should appear.
	l.Warn().Msg("warn should appear")
	l.Error().Msg("error should appear")

	content, err := os.ReadFile(cfg.Logging.File)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	out := string(content)

	for _, forbidden := range []string{"debug filtered out", "info filtered out"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("filtered message leaked: %q", forbidden)
		}
	}
	for _, required := range []string{"warn should appear", "error should appear"} {
		if !strings.Contains(out, required) {
			t.Errorf("expected message missing: %q (file: %q)", required, out)
		}
	}
}

func TestLogger_CloseIdempotent(t *testing.T) {
	tmp := t.TempDir()
	cfg := newTestConfig(t, tmp)

	logger, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := logger.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("second Close must be no-op, got: %v", err)
	}
}

func TestLogger_WithContext_CarriesTraceID(t *testing.T) {
	tmp := t.TempDir()
	cfg := newTestConfig(t, tmp)

	logger, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer logger.Close()

	ctx := WithTraceID(context.Background(), "trace-abc-123")
	child := logger.WithContext(ctx)
	child.Info().Msg("event with trace")

	content, err := os.ReadFile(cfg.Logging.File)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(content), `"trace_id":"trace-abc-123"`) {
		t.Fatalf("trace_id field missing from log: %q", string(content))
	}
}

func TestLogger_WithContext_NilOrEmpty(t *testing.T) {
	tmp := t.TempDir()
	cfg := newTestConfig(t, tmp)

	logger, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer logger.Close()

	// Nil ctx must not panic.
	_ = logger.WithContext(nil)
	// Empty trace ID must return root logger unchanged.
	_ = logger.WithContext(context.Background())
}

func TestParseLevel_Invalid(t *testing.T) {
	if _, err := parseLevel("bogus"); err == nil {
		t.Fatal("expected error for invalid level")
	}
}

func TestParseLevel_AllValid(t *testing.T) {
	valid := []string{"trace", "debug", "info", "warn", "warning", "error", "fatal", "panic", "disabled", "INFO", "  Info  "}
	for _, v := range valid {
		if _, err := parseLevel(v); err != nil {
			t.Errorf("parseLevel(%q) unexpected error: %v", v, err)
		}
	}
}
