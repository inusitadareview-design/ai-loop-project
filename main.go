// main.go is the entry point for the Wails desktop application.
// It initializes the Wails runtime, creates the App instance, and
// starts the application window.
//
// This file coexists with cmd/ailoop/main.go (CLI standalone) to
// support both use cases:
// - Desktop users: run ./build/bin/ailoop-desktop (Wails)
// - Server users: run ./bin/ailoop dashboard (Cobra CLI)
//
// Sprint 8 increment 7a: minimal setup with stub App.
package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:web/dist
var assets embed.FS

// Version is the application version. Defaults to "dev" and is
// overridden at build time via ldflags (see Makefile build target).
var Version = "dev"

func main() {
	// Create an instance of the app structure
	app := NewApp(Version)

	// Create application with options
	err := wails.Run(&options.App{
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
		println("Error:", err.Error())
	}
}
