// main.go is the entrypoint for the k8s-dashboard binary.
package main

// ---------------------------------------------------------------------------
// Author: Labiyb M. Said — DevSecOps Engineer
// Contact: abdulmunimsaid82@gmail.com
// ---------------------------------------------------------------------------

import (
	"flag"
	"log/slog"
	"os"

	"github.com/ALabiyb/k8s-dashboard/config"
	"github.com/ALabiyb/k8s-dashboard/internal/api"
)

func main() {
	// ── Structured logging ───────────────────────────────────────────────────
	// JSON to stdout: easy to ship to a log aggregator and query/alert on
	// (vs. the old free-text "[component] message" lines). Every log call in
	// this codebase goes through slog's package-level functions, which use
	// this default logger — see docs/PRODUCTION_READINESS.md §2.5.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	// ── Flags ────────────────────────────────────────────────────────────────
	configPath := flag.String("config", "config/config.yaml", "path to config.yaml")

	// Pass -mock to force mock mode even if a real cluster is available.
	// Mock mode is also activated automatically when no kubeconfig is found.
	useMock := flag.Bool("mock", false, "use fake data instead of a real k8s cluster")

	flag.Parse()

	// ── Config ───────────────────────────────────────────────────────────────
	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	slog.Info("config loaded", "component", "main",
		"port", cfg.Server.Port, "poll_interval", cfg.Server.PollInterval.String())

	// ── Server ───────────────────────────────────────────────────────────────
	server, err := api.New(cfg, *useMock)
	if err != nil {
		slog.Error("failed to create server", "error", err)
		os.Exit(1)
	}

	if err := server.Start(); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}
