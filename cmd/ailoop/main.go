// Package main is the entry point of the AI Loop Project CLI.
// It loads the settings file, initializes the structured logger, and
// dispatches execution to the appropriate subcommand. Subcommands that
// are not yet implemented print a placeholder and exit successfully,
// preserving the binary's contract while the corresponding features
// are built in later sprints (see .ai/STATUS_AND_PROCESS.md).
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/SEU_USUARIO/ai-loop-project/internal/api"
	"github.com/SEU_USUARIO/ai-loop-project/internal/engine"
	"github.com/SEU_USUARIO/ai-loop-project/internal/memory"
	"github.com/SEU_USUARIO/ai-loop-project/internal/observability"
	"github.com/SEU_USUARIO/ai-loop-project/internal/scheduler"
	"github.com/SEU_USUARIO/ai-loop-project/pkg/config"
)

// Version is the application version. It defaults to "dev" and is
// overridden at build time via ldflags (see Makefile build target).
var Version = "dev"

// defaultSettingsPath is the path to the settings file relative to
// the working directory. Override via AILOOP_SETTINGS env var.
const defaultSettingsPath = "config/settings.yaml"

// App bundles the runtime dependencies (config + logger) shared by
// every subcommand. Subcommands receive *App via closure, keeping the
// cobra.Command tree pure and testable without a real filesystem.
type App struct {
	Config *config.Config
	Logger *observability.Logger
}

// run is the real entry point. It returns an error instead of calling
// os.Exit so tests can assert on failures without forking the process.
func run() error {
	ctx := context.Background()

	settingsPath := os.Getenv("AILOOP_SETTINGS")
	if settingsPath == "" {
		settingsPath = defaultSettingsPath
	}

	cfg, err := config.Load(ctx, settingsPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger, err := observability.New(cfg)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer func() {
		if err := logger.Close(); err != nil {
			// Last-resort stderr write — logger is already closed/broken.
			fmt.Fprintf(os.Stderr, "ailoop: close logger: %v\n", err)
		}
	}()

	app := &App{Config: cfg, Logger: logger}
	log.Info().
		Str("version", Version).
		Str("environment", cfg.App.Environment).
		Msg("ailoop starting")

	root := app.newRootCmd()
	return root.ExecuteContext(ctx)
}

// newRootCmd builds the cobra command tree. Every subcommand is
// registered here; unimplemented ones use the stub helper.
func (a *App) newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "ailoop",
		Short: "AI Loop Project — autonomous AI pipeline orchestrator",
		Long: `AI Loop Project executes autonomous work loops
(plan → execute → review → publish) routing AI calls via OmniRoute,
with token optimization in Rust and orchestration in Go.

Use "ailoop [command] --help" for more information about a command.`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true, // we print errors ourselves with structured logger
	}

	root.AddCommand(
		a.newRunCmd(),
		a.newDiscoverCmd(),
		a.newModelsCmd(),
		a.newComboCmd(),
		a.newPipelinesCmd(),
		a.newMemoryCmd(),
		a.newScheduleCmd(),
		a.newBudgetCmd(),
		a.newDashboardCmd(),
		a.newStatusCmd(),
	)

	return root
}

// stub returns a *cobra.Command that prints a "not implemented"
// placeholder and exits cleanly. Used for subcommands whose real
// implementation belongs to a future sprint (R1 — one change at a time).
func stub(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Info().Str("command", use).Msg("not implemented yet")
			fmt.Fprintf(cmd.OutOrStdout(), "ailoop %s: not implemented yet (see .ai/STATUS_AND_PROCESS.md)\n", use)
			return nil
		},
	}
}

// --- Subcommand stubs (real implementations in later sprints) ---

func (a *App) newRunCmd() *cobra.Command {
	return stub("run", "Execute a named pipeline once")
}

func (a *App) newDiscoverCmd() *cobra.Command {
	return stub("discover", "Resolve provider endpoints via discovery")
}

func (a *App) newModelsCmd() *cobra.Command {
	return stub("models", "List available AI models across providers")
}

func (a *App) newComboCmd() *cobra.Command {
	return stub("combo", "Run a multi-stage combination pipeline")
}

func (a *App) newPipelinesCmd() *cobra.Command {
	return stub("pipelines", "Manage pipeline definitions")
}

func (a *App) newMemoryCmd() *cobra.Command {
	return stub("memory", "Inspect and manage persistent memory")
}

func (a *App) newScheduleCmd() *cobra.Command {
	return stub("schedule", "Manage scheduled pipeline executions")
}

func (a *App) newBudgetCmd() *cobra.Command {
	return stub("budget", "Inspect and manage daily budget state")
}

func (a *App) newDashboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dashboard",
		Short: "Start the HTTP server for the web dashboard",
		Long: `Starts the HTTP server that serves the React dashboard
and exposes the REST API at /api/v1/*. The server binds to the
host/port configured in settings.yaml (default 127.0.0.1:8080).

Press Ctrl+C to stop gracefully.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runDashboard(cmd.Context())
		},
	}
}

// runDashboard wires up the engine, scheduler, memory, and API server
// and blocks until ctx is canceled.
func (a *App) runDashboard(ctx context.Context) error {
	// Build engine (stub mode for now; Sprint 4 increment 2+ uses real).
	eng := engine.NewEngine()

	// Resolve outputs directory to absolute path so drafts are always
	// saved in the project's data/outputs/ regardless of cwd.
	outputsDir := a.Config.Paths.OutputsDir
	if outputsDir == "" {
		outputsDir = "data/outputs"
	}
	absOutputsDir, err := filepath.Abs(outputsDir)
	if err != nil {
		return fmt.Errorf("resolve outputs dir: %w", err)
	}
	eng.SetOutputsDir(absOutputsDir)

	// Build memory store.
	mem, err := memory.NewStore(a.Config.Paths.MemoryDir)
	if err != nil {
		return fmt.Errorf("init memory store: %w", err)
	}

	// Wire engine to memory store for event persistence (I5, G4.2).
	eng.SetStore(mem)

	// Build scheduler.
	sched, err := scheduler.New(eng, a.Config, a.Logger)
	if err != nil {
		return fmt.Errorf("init scheduler: %w", err)
	}

	// Build API server.
	srv, err := api.NewServer(a.Config, a.Logger, sched, mem, eng, Version)
	if err != nil {
		return fmt.Errorf("init api server: %w", err)
	}

	// Connect scheduler to WebSocket hub for real-time events
	sched.SetHub(srv.Hub())

	// Start scheduler in background.
	schedCtx, schedCancel := context.WithCancel(ctx)
	defer schedCancel()
	go func() { _ = sched.Start(schedCtx) }()

	// Listen for SIGTERM/SIGINT.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info().
		Str("host", a.Config.Server.Host).
		Int("port", a.Config.Server.Port).
		Msg("dashboard: starting HTTP server")

	return srv.ListenAndServe(ctx)
}

func (a *App) newStatusCmd() *cobra.Command {
	return stub("status", "Show runtime status of the AI Loop daemon")
}

// main is a thin wrapper around run. It exists solely to translate the
// returned error into an exit code — business logic lives in run() so
// that tests can exercise it without spawning a subprocess.
func main() {
	if err := run(); err != nil {
		// At this point the logger may already be closed by the defer
		// inside run(), so fall back to stderr for the final message.
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "ailoop: %v\n", err)
		}
		os.Exit(1)
	}
}
