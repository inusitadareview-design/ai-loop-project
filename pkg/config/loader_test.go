package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeSettingsFile writes content to a settings.yaml fixture inside a
// fresh temporary directory and returns its absolute path.
func writeSettingsFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write settings fixture: %v", err)
	}
	return path
}

// chdir switches the working directory to a fresh temporary directory for
// the duration of the test and restores the original one during cleanup.
func chdir(t *testing.T) {
	t.Helper()

	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})
}

// minimalSettings returns the smallest valid settings document, pointing
// the data directory at dataDir so tests never touch the real repository.
func minimalSettings(dataDir string) string {
	return fmt.Sprintf(`budget:
  daily_limit_usd: 1
routing:
  fallback_chain:
    - provider-a
paths:
  data_dir: %q
`, dataDir)
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T) string
		cancelCtx bool
		wantErr   string
		wantErrIs error
		check     func(t *testing.T, cfg *Config)
	}{
		{
			name: "valid_full_config",
			setup: func(t *testing.T) string {
				dataDir := filepath.Join(t.TempDir(), "runtime")
				content := fmt.Sprintf(`budget:
  daily_limit_usd: 2.5
routing:
  timeout_seconds: 45
  max_requests_retries: 2
  fallback_chain:
    - provider-a
    - provider-b
paths:
  data_dir: %q
logging:
  level: DEBUG
  max_size_mb: 20
  max_backups: 3
api:
  host: 0.0.0.0
  port: 9090
`, dataDir)
				return writeSettingsFile(t, content)
			},
			check: func(t *testing.T, cfg *Config) {
				if got := cfg.Budget.DailyLimitUSD; got != 2.5 {
					t.Fatalf("Budget.DailyLimitUSD = %v, want 2.5", got)
				}
				if got := cfg.Routing.TimeoutSeconds; got != 45 {
					t.Fatalf("Routing.TimeoutSeconds = %d, want 45", got)
				}
				if got := cfg.Routing.MaxRequestsRetries; got != 2 {
					t.Fatalf("Routing.MaxRequestsRetries = %d, want 2", got)
				}
				wantChain := []string{"provider-a", "provider-b"}
				if got := cfg.Routing.FallbackChain(); !slices.Equal(got, wantChain) {
					t.Fatalf("Routing.FallbackChain() = %v, want %v", got, wantChain)
				}
				// The returned slice must be a defensive copy.
				chain := cfg.Routing.FallbackChain()
				chain[0] = "mutated"
				if got := cfg.Routing.FallbackChain()[0]; got != "provider-a" {
					t.Fatalf("FallbackChain copy semantics broken: got %q, want %q", got, "provider-a")
				}
				if got := cfg.Logging.Level; got != "debug" {
					t.Fatalf("Logging.Level = %q, want %q (normalized to lowercase)", got, "debug")
				}
				if got := cfg.Logging.MaxSizeMB; got != 20 {
					t.Fatalf("Logging.MaxSizeMB = %d, want 20", got)
				}
				if got := cfg.Logging.MaxBackups; got != 3 {
					t.Fatalf("Logging.MaxBackups = %d, want 3", got)
				}
				if got := cfg.API.Host; got != "0.0.0.0" {
					t.Fatalf("API.Host = %q, want %q", got, "0.0.0.0")
				}
				if got := cfg.API.Port; got != 9090 {
					t.Fatalf("API.Port = %d, want 9090", got)
				}
			},
		},
		{
			name: "defaults_applied",
			setup: func(t *testing.T) string {
				// Use the default data_dir ("data") without touching the
				// repository: run Load from a temporary working directory.
				chdir(t)
				return writeSettingsFile(t, `budget:
  daily_limit_usd: 0.75
routing:
  fallback_chain:
    - provider-a
`)
			},
			check: func(t *testing.T, cfg *Config) {
				if got := cfg.Routing.TimeoutSeconds; got != defaultTimeoutSeconds {
					t.Fatalf("Routing.TimeoutSeconds = %d, want default %d", got, defaultTimeoutSeconds)
				}
				if got := cfg.Routing.MaxRequestsRetries; got != defaultMaxRequestsRetries {
					t.Fatalf("Routing.MaxRequestsRetries = %d, want default %d", got, defaultMaxRequestsRetries)
				}
				if got := cfg.Paths.DataDir; got != defaultDataDir {
					t.Fatalf("Paths.DataDir = %q, want default %q", got, defaultDataDir)
				}
				if got := cfg.Logging.Level; got != defaultLogLevel {
					t.Fatalf("Logging.Level = %q, want default %q", got, defaultLogLevel)
				}
				if got := cfg.Logging.MaxSizeMB; got != defaultLogMaxSizeMB {
					t.Fatalf("Logging.MaxSizeMB = %d, want default %d", got, defaultLogMaxSizeMB)
				}
				if got := cfg.Logging.MaxBackups; got != defaultLogMaxBackups {
					t.Fatalf("Logging.MaxBackups = %d, want default %d", got, defaultLogMaxBackups)
				}
				if got := cfg.API.Host; got != defaultAPIHost {
					t.Fatalf("API.Host = %q, want default %q", got, defaultAPIHost)
				}
				if got := cfg.API.Port; got != defaultAPIPort {
					t.Fatalf("API.Port = %d, want default %d", got, defaultAPIPort)
				}
				for _, dir := range []string{
					filepath.Join("data", "memory"),
					filepath.Join("data", "logs"),
					filepath.Join("data", "cache"),
					filepath.Join("data", "outputs"),
				} {
					info, err := os.Stat(dir)
					if err != nil {
						t.Fatalf("default directory %q not created: %v", dir, err)
					}
					if !info.IsDir() {
						t.Fatalf("%q exists but is not a directory", dir)
					}
				}
			},
		},
		{
			name: "zero_budget_is_valid",
			setup: func(t *testing.T) string {
				dataDir := filepath.Join(t.TempDir(), "runtime")
				content := fmt.Sprintf(`budget:
  daily_limit_usd: 0
routing:
  fallback_chain:
    - provider-a
paths:
  data_dir: %q
`, dataDir)
				return writeSettingsFile(t, content)
			},
			check: func(t *testing.T, cfg *Config) {
				if got := cfg.Budget.DailyLimitUSD; got != 0 {
					t.Fatalf("Budget.DailyLimitUSD = %v, want 0 (zero-cost-only mode)", got)
				}
			},
		},
		{
			name: "missing_file",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "does-not-exist.yaml")
			},
			wantErr:   "read settings file",
			wantErrIs: fs.ErrNotExist,
		},
		{
			name: "malformed_yaml",
			setup: func(t *testing.T) string {
				return writeSettingsFile(t, ":\n\t[")
			},
			wantErr: "parse settings file",
		},
		{
			name: "wrong_type",
			setup: func(t *testing.T) string {
				return writeSettingsFile(t, `budget:
  daily_limit_usd: "abc"
routing:
  fallback_chain:
    - provider-a
`)
			},
			wantErr: "parse settings file",
		},
		{
			name: "unknown_field_rejected",
			setup: func(t *testing.T) string {
				return writeSettingsFile(t, `budgett:
  daily_limit_usd: 1
`)
			},
			wantErr: "parse settings file",
		},
		{
			name: "negative_budget_rejected",
			setup: func(t *testing.T) string {
				content := fmt.Sprintf(`budget:
  daily_limit_usd: -1
routing:
  fallback_chain:
    - provider-a
paths:
  data_dir: %q
`, t.TempDir())
				return writeSettingsFile(t, content)
			},
			wantErr: "budget.daily_limit_usd",
		},
		{
			name: "zero_timeout_rejected",
			setup: func(t *testing.T) string {
				content := fmt.Sprintf(`budget:
  daily_limit_usd: 1
routing:
  timeout_seconds: 0
  fallback_chain:
    - provider-a
paths:
  data_dir: %q
`, t.TempDir())
				return writeSettingsFile(t, content)
			},
			wantErr: "routing.timeout_seconds",
		},
		{
			name: "empty_fallback_chain_rejected",
			setup: func(t *testing.T) string {
				content := fmt.Sprintf(`budget:
  daily_limit_usd: 1
routing:
  fallback_chain: []
paths:
  data_dir: %q
`, t.TempDir())
				return writeSettingsFile(t, content)
			},
			wantErr: "routing.fallback_chain",
		},
		{
			name: "invalid_log_level_rejected",
			setup: func(t *testing.T) string {
				content := fmt.Sprintf(`budget:
  daily_limit_usd: 1
routing:
  fallback_chain:
    - provider-a
paths:
  data_dir: %q
logging:
  level: verbose
`, t.TempDir())
				return writeSettingsFile(t, content)
			},
			wantErr: "logging.level",
		},
		{
			name: "data_directories_created",
			setup: func(t *testing.T) string {
				dataDir := filepath.Join(t.TempDir(), "nested", "data")
				return writeSettingsFile(t, minimalSettings(dataDir))
			},
			check: func(t *testing.T, cfg *Config) {
				for _, dir := range []string{
					cfg.Paths.MemoryDir(),
					cfg.Paths.LogsDir(),
					cfg.Paths.CacheDir(),
					cfg.Paths.OutputsDir(),
				} {
					info, err := os.Stat(dir)
					if err != nil {
						t.Fatalf("directory %q not created: %v", dir, err)
					}
					if !info.IsDir() {
						t.Fatalf("%q exists but is not a directory", dir)
					}
				}
			},
		},
		{
			name: "canceled_context",
			setup: func(t *testing.T) string {
				return writeSettingsFile(t, minimalSettings(t.TempDir()))
			},
			cancelCtx: true,
			wantErr:   "load canceled",
			wantErrIs: context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t)

			ctx := context.Background()
			if tt.cancelCtx {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			cfg, err := Load(ctx, path)

			if tt.wantErr != "" || tt.wantErrIs != nil {
				if err == nil {
					t.Fatalf("Load() error = nil, want error containing %q", tt.wantErr)
				}
				if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %q, want it to contain %q", err.Error(), tt.wantErr)
				}
				if tt.wantErrIs != nil && !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("Load() error = %q, want errors.Is(err, %v) to be true", err.Error(), tt.wantErrIs)
				}
				return
			}

			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}