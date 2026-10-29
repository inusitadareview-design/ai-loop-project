package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMinimalSettings creates a valid settings.yaml inside a temp dir
// and returns its absolute path. Used to let run() succeed during tests.
func writeMinimalSettings(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	content := `budget:
  daily_limit_usd: 0
routing:
  fallback_chain:
    - omniroute
paths:
  memory_dir: ` + filepath.Join(dataDir, "memory") + `
  outputs_dir: ` + filepath.Join(dataDir, "outputs") + `
  logs_dir: ` + filepath.Join(dataDir, "logs") + `
  cache_dir: ` + filepath.Join(dataDir, "cache") + `
`
	path := filepath.Join(dir, "settings.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	return path
}

func TestRootCmd_Help(t *testing.T) {
	app := &App{}
	cmd := app.newRootCmd()

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("help: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"ailoop", "run", "discover", "dashboard", "status", "budget"} {
		if !strings.Contains(out, want) {
			t.Errorf("help output missing %q:\n%s", want, out)
		}
	}
}

func TestRootCmd_Version(t *testing.T) {
	previous := Version
	Version = "test-0.1.0"
	defer func() { Version = previous }()

	app := &App{}
	cmd := app.newRootCmd()

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--version"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("version: %v", err)
	}

	if !strings.Contains(buf.String(), "test-0.1.0") {
		t.Fatalf("version output missing tag: %q", buf.String())
	}
}

func TestStubSubcommands(t *testing.T) {
	app := &App{}
	root := app.newRootCmd()

	// Commands that have real implementations and require initialized
	// dependencies (config, logger, etc.) cannot be tested as stubs.
	// They have their own dedicated tests in their respective packages.
	implemented := map[string]bool{
		"dashboard": true, // Sprint 7: real HTTP server via runDashboard()
	}

	for _, sub := range root.Commands() {
		t.Run(sub.Name(), func(t *testing.T) {
			if implemented[sub.Name()] {
				t.Skipf("subcommand %q has real implementation; tested in its own package", sub.Name())
			}

			var buf bytes.Buffer
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetArgs([]string{sub.Name()})

			if err := root.Execute(); err != nil {
				t.Fatalf("subcommand %q: %v", sub.Name(), err)
			}
			if !strings.Contains(buf.String(), "not implemented yet") {
				t.Errorf("subcommand %q output missing placeholder: %q", sub.Name(), buf.String())
			}
		})
	}
}

func TestRun_FailsWithMissingSettings(t *testing.T) {
	// Point AILOOP_SETTINGS at a non-existent path and expect an error
	// that mentions config loading.
	previous := os.Getenv("AILOOP_SETTINGS")
	t.Setenv("AILOOP_SETTINGS", filepath.Join(t.TempDir(), "missing.yaml"))
	defer os.Setenv("AILOOP_SETTINGS", previous)

	err := run()
	if err == nil {
		t.Fatal("run() must fail when settings file is missing")
	}
	if !strings.Contains(err.Error(), "load config") {
		t.Fatalf("error should mention 'load config', got: %v", err)
	}
}

func TestRun_SucceedsWithValidSettings(t *testing.T) {
	// The full run() exits after executing the root command with no
	// args (which prints help and returns nil). Use AILOOP_SETTINGS to
	// point at a valid file and confirm no error bubbles up.
	path := writeMinimalSettings(t)

	previous := os.Getenv("AILOOP_SETTINGS")
	t.Setenv("AILOOP_SETTINGS", path)
	defer os.Setenv("AILOOP_SETTINGS", previous)

	// Override os.Args so root.Execute() prints help and returns nil.
	prevArgs := os.Args
	os.Args = []string{"ailoop", "--help"}
	defer func() { os.Args = prevArgs }()

	if err := run(); err != nil {
		t.Fatalf("run(): %v", err)
	}
}
