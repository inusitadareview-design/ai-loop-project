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

func writeSettingsFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write settings fixture: %v", err)
	}
	return path
}

func minimalSettings(dataDir string) string {
	return fmt.Sprintf(`
budget:
  daily_limit_usd: 1
routing:
  fallback_chain:
    - omniroute
paths:
  memory_dir: %q
  outputs_dir: %q
  logs_dir: %q
  cache_dir: %q
`, dataDir, dataDir, dataDir, dataDir)
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
				content := fmt.Sprintf(`
app:
  name: ai-loop
  version: "0.2.0"
  environment: production
providers:
  default: omniroute
  timeout_seconds: 45
  list:
    - name: omniroute
      base_url: https://api.omniroute.ai/v1
      free_tier: true
      rate_limit_rpm: 20
routing:
  strategy: fallback
  timeout_seconds: 60
  max_requests_retries: 5
  fallback_chain: [omniroute, openrouter]
budget:
  daily_limit_usd: 2.5
logging:
  level: DEBUG
  file: %q
  max_size_mb: 20
  max_backups: 3
  json: true
paths:
  memory_dir: %q
  outputs_dir: %q
  logs_dir: %q
  cache_dir: %q
  pipelines_dir: %q
scheduler:
  enabled: true
  timezone: America/Sao_Paulo
pipeline:
  require_review: true
server:
  host: 0.0.0.0
  port: 9090
`, filepath.Join(dataDir, "logs", "ai_loop.log"), dataDir, dataDir, filepath.Join(dataDir, "logs"), dataDir, filepath.Join("config", "pipelines"))
				return writeSettingsFile(t, content)
			},
			check: func(t *testing.T, cfg *Config) {
				if cfg.App.Environment != "production" {
					t.Fatalf("App.Environment = %q, want production", cfg.App.Environment)
				}
				if cfg.Budget.DailyLimitUSD != 2.5 {
					t.Fatalf("Budget.DailyLimitUSD = %v, want 2.5", cfg.Budget.DailyLimitUSD)
				}
				if cfg.Logging.Level != "debug" {
					t.Fatalf("Logging.Level = %q, want debug", cfg.Logging.Level)
				}
				if cfg.Server.Port != 9090 {
					t.Fatalf("Server.Port = %d, want 9090", cfg.Server.Port)
				}
				chain := cfg.Routing.FallbackChain()
				if !slices.Equal(chain, []string{"omniroute", "openrouter"}) {
					t.Fatalf("FallbackChain = %v", chain)
				}
			},
		},
		{
			name: "defaults_applied",
			setup: func(t *testing.T) string {
				dataDir := filepath.Join(t.TempDir(), "data")
				return writeSettingsFile(t, minimalSettings(dataDir))
			},
			check: func(t *testing.T, cfg *Config) {
				if cfg.App.Environment != "development" {
					t.Fatalf("App.Environment default failed")
				}
				if cfg.Routing.Strategy != "fallback" {
					t.Fatalf("Routing.Strategy default failed")
				}
				if cfg.Logging.Level != "info" {
					t.Fatalf("Logging.Level default failed")
				}
				if !cfg.Logging.JSON {
					t.Fatalf("Logging.JSON default failed")
				}
				if !cfg.Pipeline.RequireReview {
					t.Fatalf("Pipeline.RequireReview default failed")
				}
				if cfg.Server.Host != "127.0.0.1" {
					t.Fatalf("Server.Host default failed")
				}
			},
		},
		{
			name: "missing_file",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "missing.yaml")
			},
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
			name: "unknown_field_rejected",
			setup: func(t *testing.T) string {
				return writeSettingsFile(t, "unknown_key: true\n"+minimalSettings(t.TempDir()))
			},
			wantErr: "parse settings file",
		},
		{
			name: "negative_budget_rejected",
			setup: func(t *testing.T) string {
				dataDir := t.TempDir()
				content := fmt.Sprintf(`budget:
  daily_limit_usd: -1
routing:
  fallback_chain:
    - omniroute
paths:
  memory_dir: %q
  outputs_dir: %q
  logs_dir: %q
  cache_dir: %q
`, dataDir, dataDir, dataDir, dataDir)
				return writeSettingsFile(t, content)
			},
			wantErr: "budget.daily_limit_usd",
		},
		{
			name: "invalid_environment_rejected",
			setup: func(t *testing.T) string {
				dataDir := t.TempDir()
				content := fmt.Sprintf(`app:
  environment: invalid
routing:
  fallback_chain:
    - omniroute
paths:
  memory_dir: %q
  outputs_dir: %q
  logs_dir: %q
  cache_dir: %q
`, dataDir, dataDir, dataDir, dataDir)
				return writeSettingsFile(t, content)
			},
			wantErr: "app.environment",
		},
		{
			name: "canceled_context",
			setup: func(t *testing.T) string {
				return writeSettingsFile(t, minimalSettings(t.TempDir()))
			},
			cancelCtx: true,
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
					t.Fatalf("expected error")
				}
				if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q missing %q", err, tt.wantErr)
				}
				if tt.wantErrIs != nil && !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("error %v not %v", err, tt.wantErrIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}
