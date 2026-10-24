// Package config loads, validates, and applies defaults to the AI Loop
// Project settings file (config/settings.yaml). It is the single entry
// point for configuration at startup.
package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// validLogLevels lists the accepted values for logging.level after
// normalization to lowercase.
var validLogLevels = map[string]bool{
	"trace": true,
	"debug": true,
	"info":  true,
	"warn":  true,
	"error": true,
}

// Default values applied when a field is absent from the settings file.
const (
	defaultTimeoutSeconds     = 30
	defaultMaxRequestsRetries = 3
	defaultDataDir            = "data"
	defaultLogLevel           = "info"
	defaultLogMaxSizeMB       = 10
	defaultLogMaxBackups      = 5
	defaultAPIHost            = "127.0.0.1"
	defaultAPIPort            = 8080
)

// Hard limits derived from the project guardrails.
const (
	// maxRequestsRetriesCap enforces guardrail G1.2: at most 5 attempts
	// for retryable errors such as HTTP 429.
	maxRequestsRetriesCap = 5

	// maxLogBackupsCap enforces guardrail G3.3: rotation keeps at most
	// 5 backup files.
	maxLogBackupsCap = 5

	dirPermission = 0o750
)

// Config is the immutable runtime configuration of the AI Loop Project.
// Obtain it once via Load at startup and treat it as read-only for the
// whole process lifetime.
type Config struct {
	Budget  Budget
	Routing Routing
	Paths   Paths
	Logging Logging
	API     API
}

// Budget holds the financial guardrails applied to every AI call.
type Budget struct {
	// DailyLimitUSD is the maximum amount (in USD) spendable on AI calls
	// per day. Zero enables zero-cost-only mode. It is never ignored
	// (guardrail G1.1).
	DailyLimitUSD float64
}

// Routing holds provider routing parameters for AI calls.
type Routing struct {
	// TimeoutSeconds is the maximum duration of a single AI request.
	TimeoutSeconds int

	// MaxRequestsRetries is the maximum number of retries for retryable
	// errors such as HTTP 429.
	MaxRequestsRetries int

	// fallbackChain is the provider fallback chain in priority order. It
	// is unexported so callers can only access it through FallbackChain,
	// which returns a defensive copy.
	fallbackChain []string
}

// FallbackChain returns a copy of the provider fallback chain in priority
// order. The returned slice is safe to modify.
func (r Routing) FallbackChain() []string {
	out := make([]string, len(r.fallbackChain))
	copy(out, r.fallbackChain)
	return out
}

// Paths holds the base directory for all runtime data. Every file written
// at runtime stays inside DataDir (guardrail G2.2).
type Paths struct {
	// DataDir is the root directory for all runtime data.
	DataDir string
}

// MemoryDir returns the directory for semantic memories.
func (p Paths) MemoryDir() string { return filepath.Join(p.DataDir, "memory") }

// LogsDir returns the directory for rotated log files.
func (p Paths) LogsDir() string { return filepath.Join(p.DataDir, "logs") }

// CacheDir returns the directory for the SQLite semantic cache.
func (p Paths) CacheDir() string { return filepath.Join(p.DataDir, "cache") }

// OutputsDir returns the directory for generated artifacts.
func (p Paths) OutputsDir() string { return filepath.Join(p.DataDir, "outputs") }

// Logging holds log output parameters.
type Logging struct {
	// Level is the minimum log level: trace, debug, info, warn or error.
	Level string

	// MaxSizeMB is the maximum size of a single log file in megabytes.
	MaxSizeMB int

	// MaxBackups is the maximum number of rotated log files kept on disk.
	MaxBackups int
}

// API holds the parameters of the local API and dashboard server.
type API struct {
	// Host is the network interface the server binds to.
	Host string

	// Port is the TCP port the server listens on.
	Port int
}

// fileConfig mirrors the on-disk YAML schema. It is unexported and exists
// only for decoding; Load converts it into an immutable Config. Pointer
// fields distinguish "absent" (apply default) from "explicit zero value"
// (validate and possibly reject).
type fileConfig struct {
	Budget  fileBudget  `yaml:"budget"`
	Routing fileRouting `yaml:"routing"`
	Paths   filePaths   `yaml:"paths"`
	Logging fileLogging `yaml:"logging"`
	API     fileAPI     `yaml:"api"`
}

type fileBudget struct {
	DailyLimitUSD *float64 `yaml:"daily_limit_usd"`
}

type fileRouting struct {
	TimeoutSeconds     *int     `yaml:"timeout_seconds"`
	MaxRequestsRetries *int     `yaml:"max_requests_retries"`
	FallbackChain      []string `yaml:"fallback_chain"`
}

type filePaths struct {
	DataDir *string `yaml:"data_dir"`
}

type fileLogging struct {
	Level      *string `yaml:"level"`
	MaxSizeMB  *int    `yaml:"max_size_mb"`
	MaxBackups *int    `yaml:"max_backups"`
}

type fileAPI struct {
	Host *string `yaml:"host"`
	Port *int    `yaml:"port"`
}

// Load reads the YAML settings file at path, applies defaults, validates
// every value, ensures the data directories exist, and returns an
// immutable Config. It returns an error if the context is canceled, if
// the file is missing, malformed or contains unknown fields, if any value
// fails validation, or if the data directories cannot be created.
func Load(ctx context.Context, path string) (*Config, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("config: load canceled: %w", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read settings file %q: %w", path, err)
	}

	var fc fileConfig
	if err := decodeStrict(raw, &fc); err != nil {
		return nil, fmt.Errorf("config: parse settings file %q: %w", path, err)
	}

	cfg, err := buildConfig(fc)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	if err := createDataDirs(cfg.Paths); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("config: load canceled: %w", err)
	}

	return cfg, nil
}

// decodeStrict decodes raw YAML into fc, rejecting unknown fields at every
// level so typos in the settings file fail fast instead of being ignored.
func decodeStrict(raw []byte, fc *fileConfig) error {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)

	if err := dec.Decode(fc); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("settings file is empty")
		}
		return err
	}
	return nil
}

// buildConfig applies defaults to fc and validates every field. It returns
// an immutable Config or an explicit error naming the offending field.
func buildConfig(fc fileConfig) (*Config, error) {
	budget, err := buildBudget(fc.Budget)
	if err != nil {
		return nil, err
	}

	routing, err := buildRouting(fc.Routing)
	if err != nil {
		return nil, err
	}

	paths, err := buildPaths(fc.Paths)
	if err != nil {
		return nil, err
	}

	logging, err := buildLogging(fc.Logging)
	if err != nil {
		return nil, err
	}

	apiCfg, err := buildAPI(fc.API)
	if err != nil {
		return nil, err
	}

	return &Config{
		Budget:  budget,
		Routing: routing,
		Paths:   paths,
		Logging: logging,
		API:     apiCfg,
	}, nil
}

func buildBudget(fb fileBudget) (Budget, error) {
	if fb.DailyLimitUSD == nil {
		return Budget{}, errors.New("invalid value for budget.daily_limit_usd: field is required")
	}
	if *fb.DailyLimitUSD < 0 {
		return Budget{}, fmt.Errorf("invalid value for budget.daily_limit_usd: must be >= 0, got %v", *fb.DailyLimitUSD)
	}
	return Budget{DailyLimitUSD: *fb.DailyLimitUSD}, nil
}

func buildRouting(fr fileRouting) (Routing, error) {
	timeoutSeconds := defaultTimeoutSeconds
	if fr.TimeoutSeconds != nil {
		timeoutSeconds = *fr.TimeoutSeconds
	}
	if timeoutSeconds <= 0 {
		return Routing{}, fmt.Errorf("invalid value for routing.timeout_seconds: must be > 0, got %d", timeoutSeconds)
	}

	maxRetries := defaultMaxRequestsRetries
	if fr.MaxRequestsRetries != nil {
		maxRetries = *fr.MaxRequestsRetries
	}
	if maxRetries < 0 || maxRetries > maxRequestsRetriesCap {
		return Routing{}, fmt.Errorf("invalid value for routing.max_requests_retries: must be in [0, %d], got %d", maxRequestsRetriesCap, maxRetries)
	}

	chain := make([]string, 0, len(fr.FallbackChain))
	for _, provider := range fr.FallbackChain {
		trimmed := strings.TrimSpace(provider)
		if trimmed == "" {
			return Routing{}, errors.New("invalid value for routing.fallback_chain: entries must be non-empty")
		}
		chain = append(chain, trimmed)
	}
	if len(chain) == 0 {
		return Routing{}, errors.New("invalid value for routing.fallback_chain: must contain at least one provider")
	}

	return Routing{
		TimeoutSeconds:     timeoutSeconds,
		MaxRequestsRetries: maxRetries,
		fallbackChain:      chain,
	}, nil
}

func buildPaths(fp filePaths) (Paths, error) {
	dataDir := defaultDataDir
	if fp.DataDir != nil {
		dataDir = strings.TrimSpace(*fp.DataDir)
	}
	if dataDir == "" {
		return Paths{}, errors.New("invalid value for paths.data_dir: must be non-empty")
	}
	return Paths{DataDir: dataDir}, nil
}

func buildLogging(fl fileLogging) (Logging, error) {
	level := defaultLogLevel
	if fl.Level != nil {
		level = strings.ToLower(strings.TrimSpace(*fl.Level))
	}
	if !validLogLevels[level] {
		return Logging{}, fmt.Errorf("invalid value for logging.level: must be one of trace, debug, info, warn, error, got %q", level)
	}

	maxSizeMB := defaultLogMaxSizeMB
	if fl.MaxSizeMB != nil {
		maxSizeMB = *fl.MaxSizeMB
	}
	if maxSizeMB <= 0 {
		return Logging{}, fmt.Errorf("invalid value for logging.max_size_mb: must be > 0, got %d", maxSizeMB)
	}

	maxBackups := defaultLogMaxBackups
	if fl.MaxBackups != nil {
		maxBackups = *fl.MaxBackups
	}
	if maxBackups < 1 || maxBackups > maxLogBackupsCap {
		return Logging{}, fmt.Errorf("invalid value for logging.max_backups: must be in [1, %d], got %d", maxLogBackupsCap, maxBackups)
	}

	return Logging{Level: level, MaxSizeMB: maxSizeMB, MaxBackups: maxBackups}, nil
}

func buildAPI(fa fileAPI) (API, error) {
	host := defaultAPIHost
	if fa.Host != nil {
		host = strings.TrimSpace(*fa.Host)
	}
	if host == "" {
		return API{}, errors.New("invalid value for api.host: must be non-empty")
	}

	port := defaultAPIPort
	if fa.Port != nil {
		port = *fa.Port
	}
	if port < 1 || port > 65535 {
		return API{}, fmt.Errorf("invalid value for api.port: must be in [1, 65535], got %d", port)
	}

	return API{Host: host, Port: port}, nil
}

// createDataDirs ensures the data directory tree exists. All runtime
// writes stay inside these directories (guardrail G2.2).
func createDataDirs(p Paths) error {
	dirs := []string{p.DataDir, p.MemoryDir(), p.LogsDir(), p.CacheDir(), p.OutputsDir()}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, dirPermission); err != nil {
			return fmt.Errorf("create directory %q: %w", dir, err)
		}
	}
	return nil
}