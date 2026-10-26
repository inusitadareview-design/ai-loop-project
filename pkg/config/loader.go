package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

var validEnvironments = map[string]bool{"development": true, "staging": true, "production": true}
var validStrategies = map[string]bool{"fallback": true, "cheapest": true, "fastest": true}
var validLogLevels = map[string]bool{"trace": true, "debug": true, "info": true, "warn": true, "error": true}

// Config is the immutable runtime configuration of the AI Loop Project.
type Config struct {
	App       App
	Providers Providers
	Routing   Routing
	Budget    Budget
	Logging   Logging
	Paths     Paths
	Scheduler Scheduler
	Pipeline  Pipeline
	Server    Server
}

type App struct {
	Name        string
	Version     string
	Environment string
}

type Provider struct {
	Name         string
	BaseURL      string
	FreeTier     bool
	RateLimitRPM int
}

type Providers struct {
	Default        string
	TimeoutSeconds int
	MaxRetries     int
	list           []Provider
}

// List returns a defensive copy of the configured providers.
func (p Providers) List() []Provider {
	out := make([]Provider, len(p.list))
	copy(out, p.list)
	return out
}

type Routing struct {
	Strategy           string
	fallbackChain      []string
	TimeoutSeconds     int
	MaxRequestsRetries int
}

// FallbackChain returns a defensive copy of the provider fallback chain.
func (r Routing) FallbackChain() []string {
	out := make([]string, len(r.fallbackChain))
	copy(out, r.fallbackChain)
	return out
}

type Budget struct {
	DailyLimitUSD         float64
	MaxRequestsPerMinute  int
	CostPer1kInputTokens  float64
	CostPer1kOutputTokens float64
	BackoffBaseSeconds    int
}

type Logging struct {
	Level      string
	File       string
	MaxSizeMB  int
	MaxBackups int
	JSON       bool
}

type Paths struct {
	MemoryDir    string
	OutputsDir   string
	LogsDir      string
	CacheDir     string
	PipelinesDir string
}

type Scheduler struct {
	Enabled                bool
	Timezone               string
	MaxConcurrentPipelines int
	LockStaleSeconds       int
}

type Pipeline struct {
	RequireReview    bool
	MaxIterations    int
	DeadLoopFailures int
}

type Server struct {
	Host string
	Port int
}

// --- YAML Decoding Structs ---

type fileConfig struct {
	App       fileApp       `yaml:"app"`
	Providers fileProviders `yaml:"providers"`
	Routing   fileRouting   `yaml:"routing"`
	Budget    fileBudget    `yaml:"budget"`
	Logging   fileLogging   `yaml:"logging"`
	Paths     filePaths     `yaml:"paths"`
	Scheduler fileScheduler `yaml:"scheduler"`
	Pipeline  filePipeline  `yaml:"pipeline"`
	Server    fileServer    `yaml:"server"`
}

type fileApp struct {
	Name        *string `yaml:"name"`
	Version     *string `yaml:"version"`
	Environment *string `yaml:"environment"`
}

type fileProvider struct {
	Name         *string `yaml:"name"`
	BaseURL      *string `yaml:"base_url"`
	FreeTier     *bool   `yaml:"free_tier"`
	RateLimitRPM *int    `yaml:"rate_limit_rpm"`
}

type fileProviders struct {
	Default        *string        `yaml:"default"`
	TimeoutSeconds *int           `yaml:"timeout_seconds"`
	MaxRetries     *int           `yaml:"max_retries"`
	List           []fileProvider `yaml:"list"`
}

type fileRouting struct {
	Strategy           *string  `yaml:"strategy"`
	FallbackChain      []string `yaml:"fallback_chain"`
	TimeoutSeconds     *int     `yaml:"timeout_seconds"`
	MaxRequestsRetries *int     `yaml:"max_requests_retries"`
}

type fileBudget struct {
	DailyLimitUSD         *float64 `yaml:"daily_limit_usd"`
	MaxRequestsPerMinute  *int     `yaml:"max_requests_per_minute"`
	CostPer1kInputTokens  *float64 `yaml:"cost_per_1k_input_tokens"`
	CostPer1kOutputTokens *float64 `yaml:"cost_per_1k_output_tokens"`
	BackoffBaseSeconds    *int     `yaml:"backoff_base_seconds"`
}

type fileLogging struct {
	Level      *string `yaml:"level"`
	File       *string `yaml:"file"`
	MaxSizeMB  *int    `yaml:"max_size_mb"`
	MaxBackups *int    `yaml:"max_backups"`
	JSON       *bool   `yaml:"json"`
}

type filePaths struct {
	MemoryDir    *string `yaml:"memory_dir"`
	OutputsDir   *string `yaml:"outputs_dir"`
	LogsDir      *string `yaml:"logs_dir"`
	CacheDir     *string `yaml:"cache_dir"`
	PipelinesDir *string `yaml:"pipelines_dir"`
}

type fileScheduler struct {
	Enabled                *bool   `yaml:"enabled"`
	Timezone               *string `yaml:"timezone"`
	MaxConcurrentPipelines *int    `yaml:"max_concurrent_pipelines"`
	LockStaleSeconds       *int    `yaml:"lock_stale_seconds"`
}

type filePipeline struct {
	RequireReview    *bool `yaml:"require_review"`
	MaxIterations    *int  `yaml:"max_iterations"`
	DeadLoopFailures *int  `yaml:"dead_loop_failures"`
}

type fileServer struct {
	Host *string `yaml:"host"`
	Port *int    `yaml:"port"`
}

// --- Defaults ---

const (
	defaultAppEnv         = "development"
	defaultProvTimeout    = 30
	defaultProvMaxRetries = 3
	defaultRoutStrategy   = "fallback"
	defaultRoutTimeout    = 30
	defaultRoutMaxRetries = 5
	defaultBudgetLimit    = 0.0
	defaultBudgetRPM      = 20
	defaultBudgetBackoff  = 2
	defaultLogLevel       = "info"
	defaultLogFile        = "data/logs/ai_loop.log"
	defaultLogMaxSize     = 10
	defaultLogMaxBackups  = 5
	defaultLogJSON        = true
	defaultPathMemory     = "data/memory"
	defaultPathOutputs    = "data/outputs"
	defaultPathLogs       = "data/logs"
	defaultPathCache      = "data/cache"
	defaultPathPipelines  = "config/pipelines"
	defaultSchedTimezone  = "UTC"
	defaultSchedMaxConc   = 1
	defaultSchedStale     = 3600
	defaultPipMaxIter     = 50
	defaultPipDeadLoop    = 3
	defaultServerHost     = "127.0.0.1"
	defaultServerPort     = 8080
)

// Load reads the YAML settings file, applies defaults, validates, and ensures directories exist.
func Load(ctx context.Context, path string) (*Config, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("config: load canceled: %w", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read settings file %q: %w", path, err)
	}

	var fc fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // Strict decoding: rejects typos/unknown fields
	if err := dec.Decode(&fc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("config: parse settings file %q: settings file is empty", path)
		}
		return nil, fmt.Errorf("config: parse settings file %q: %w", path, err)
	}

	cfg, err := buildConfig(fc)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	if err := createDataDirs(cfg.Paths); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	return cfg, nil
}

func buildConfig(fc fileConfig) (*Config, error) {
	app := App{
		Name:        strVal(fc.App.Name, "ai-loop-project"),
		Version:     strVal(fc.App.Version, "0.1.0"),
		Environment: strVal(fc.App.Environment, defaultAppEnv),
	}
	if !validEnvironments[app.Environment] {
		return nil, fmt.Errorf("invalid value for app.environment: must be one of development, staging, production, got %q", app.Environment)
	}

	prov := Providers{
		Default:        strVal(fc.Providers.Default, "omniroute"),
		TimeoutSeconds: intVal(fc.Providers.TimeoutSeconds, defaultProvTimeout),
		MaxRetries:     intVal(fc.Providers.MaxRetries, defaultProvMaxRetries),
	}
	for _, fp := range fc.Providers.List {
		prov.list = append(prov.list, Provider{
			Name:         strVal(fp.Name, ""),
			BaseURL:      strVal(fp.BaseURL, ""),
			FreeTier:     boolVal(fp.FreeTier, true),
			RateLimitRPM: intVal(fp.RateLimitRPM, 20),
		})
	}

	rout := Routing{
		Strategy:           strVal(fc.Routing.Strategy, defaultRoutStrategy),
		TimeoutSeconds:     intVal(fc.Routing.TimeoutSeconds, defaultRoutTimeout),
		MaxRequestsRetries: intVal(fc.Routing.MaxRequestsRetries, defaultRoutMaxRetries),
	}
	if !validStrategies[rout.Strategy] {
		return nil, fmt.Errorf("invalid value for routing.strategy: got %q", rout.Strategy)
	}
	for _, p := range fc.Routing.FallbackChain {
		if p != "" {
			rout.fallbackChain = append(rout.fallbackChain, p)
		}
	}
	if len(rout.fallbackChain) == 0 {
		return nil, errors.New("invalid value for routing.fallback_chain: must contain at least one provider")
	}

	bud := Budget{
		DailyLimitUSD:         floatVal(fc.Budget.DailyLimitUSD, defaultBudgetLimit),
		MaxRequestsPerMinute:  intVal(fc.Budget.MaxRequestsPerMinute, defaultBudgetRPM),
		CostPer1kInputTokens:  floatVal(fc.Budget.CostPer1kInputTokens, 0.0),
		CostPer1kOutputTokens: floatVal(fc.Budget.CostPer1kOutputTokens, 0.0),
		BackoffBaseSeconds:    intVal(fc.Budget.BackoffBaseSeconds, defaultBudgetBackoff),
	}
	if bud.DailyLimitUSD < 0 {
		return nil, errors.New("invalid value for budget.daily_limit_usd: must be >= 0")
	}

	log := Logging{
		Level:      strings.ToLower(strVal(fc.Logging.Level, defaultLogLevel)),
		File:       strVal(fc.Logging.File, defaultLogFile),
		MaxSizeMB:  intVal(fc.Logging.MaxSizeMB, defaultLogMaxSize),
		MaxBackups: intVal(fc.Logging.MaxBackups, defaultLogMaxBackups),
		JSON:       boolVal(fc.Logging.JSON, defaultLogJSON),
	}
	if !validLogLevels[log.Level] {
		return nil, fmt.Errorf("invalid value for logging.level: got %q", log.Level)
	}

	pat := Paths{
		MemoryDir:    strVal(fc.Paths.MemoryDir, defaultPathMemory),
		OutputsDir:   strVal(fc.Paths.OutputsDir, defaultPathOutputs),
		LogsDir:      strVal(fc.Paths.LogsDir, defaultPathLogs),
		CacheDir:     strVal(fc.Paths.CacheDir, defaultPathCache),
		PipelinesDir: strVal(fc.Paths.PipelinesDir, defaultPathPipelines),
	}

	sch := Scheduler{
		Enabled:                boolVal(fc.Scheduler.Enabled, false),
		Timezone:               strVal(fc.Scheduler.Timezone, defaultSchedTimezone),
		MaxConcurrentPipelines: intVal(fc.Scheduler.MaxConcurrentPipelines, defaultSchedMaxConc),
		LockStaleSeconds:       intVal(fc.Scheduler.LockStaleSeconds, defaultSchedStale),
	}

	pip := Pipeline{
		RequireReview:    boolVal(fc.Pipeline.RequireReview, true),
		MaxIterations:    intVal(fc.Pipeline.MaxIterations, defaultPipMaxIter),
		DeadLoopFailures: intVal(fc.Pipeline.DeadLoopFailures, defaultPipDeadLoop),
	}

	srv := Server{
		Host: strVal(fc.Server.Host, defaultServerHost),
		Port: intVal(fc.Server.Port, defaultServerPort),
	}

	return &Config{
		App: app, Providers: prov, Routing: rout, Budget: bud,
		Logging: log, Paths: pat, Scheduler: sch, Pipeline: pip, Server: srv,
	}, nil
}

func strVal(p *string, def string) string {
	if p == nil {
		return def
	}
	return strings.TrimSpace(*p)
}
func intVal(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}
func floatVal(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}
func boolVal(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func createDataDirs(p Paths) error {
	dirs := []string{p.MemoryDir, p.OutputsDir, p.LogsDir, p.CacheDir}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create directory %q: %w", dir, err)
		}
	}
	return nil
}
