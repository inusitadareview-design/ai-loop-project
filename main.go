// main.go is the entry point for the Wails desktop application.
// It initializes the Wails runtime, creates the App instance, and
// starts the application window.
//
// This file coexists with cmd/ailoop/main.go (CLI standalone) to
// support both use cases:
// - Desktop users: run ./build/bin/ailoop-desktop (Wails)
// - Server users: run ./bin/ailoop dashboard (Cobra CLI)
//
// Sprint 8 increment 7c: wires scheduler and config into App for
// pipeline bindings (ListPipelines, RunPipeline, ResetFailures).
package main

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/SEU_USUARIO/ai-loop-project/internal/engine"
	"github.com/SEU_USUARIO/ai-loop-project/internal/memory"
	"github.com/SEU_USUARIO/ai-loop-project/internal/observability"
	"github.com/SEU_USUARIO/ai-loop-project/internal/scheduler"
	"github.com/SEU_USUARIO/ai-loop-project/pkg/config"
)

//go:embed all:web/dist
var assets embed.FS

// Version is the application version. Defaults to "dev" and is
// overridden at build time via ldflags (see Makefile build target).
var Version = "dev"

// defaultSettingsPath is the path to the settings file relative to
// the working directory. Override via AILOOP_SETTINGS env var.
const defaultSettingsPath = "config/settings.yaml"

func main() {
	ctx := context.Background()

	// Load configuration.
	settingsPath := os.Getenv("AILOOP_SETTINGS")
	if settingsPath == "" {
		settingsPath = defaultSettingsPath
	}
	cfg, err := config.Load(ctx, settingsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ailoop: load config: %v\n", err)
		os.Exit(1)
	}

	// Initialize structured logger.
	logger, err := observability.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ailoop: init logger: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := logger.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "ailoop: close logger: %v\n", err)
		}
	}()

	log.Info().
		Str("version", Version).
		Str("environment", cfg.App.Environment).
		Msg("ailoop desktop: starting")

	// Build engine (stub mode for now; Sprint 4 increment 2+ uses real).
	eng := engine.NewEngine()

	// Resolve outputs directory to absolute path.
	outputsDir := cfg.Paths.OutputsDir
	if outputsDir == "" {
		outputsDir = "data/outputs"
	}
	absOutputsDir, err := filepath.Abs(outputsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ailoop: resolve outputs dir: %v\n", err)
		os.Exit(1)
	}
	eng.SetOutputsDir(absOutputsDir)

	// Build memory store.
	mem, err := memory.NewStore(cfg.Paths.MemoryDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ailoop: init memory store: %v\n", err)
		os.Exit(1)
	}

	// Wire engine to memory store for event persistence (I5, G4.2).
	eng.SetStore(mem)

	// Build scheduler.
	sched, err := scheduler.New(eng, cfg, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ailoop: init scheduler: %v\n", err)
		os.Exit(1)
	}

	// Create App instance with all dependencies wired.
	app := NewApp(Version, sched, cfg)

	// Start Wails application.
	err = wails.Run(&options.App{
		Title:  "AI Loop - Autonomous AI Pipeline Orchestrator",
		Width:  1280,
		Height: 800,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 15, G: 23, B: 42, A: 1}, // #0f172a (Tailwind slate-900)
		OnStartup:        app.startup,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		fmt.Fprintf(os.Stderr, "ailoop: wails run: %v\n", err)
		os.Exit(1)
	}
}
