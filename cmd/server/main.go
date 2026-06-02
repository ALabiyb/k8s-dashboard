// main.go is the entrypoint for the k8s-dashboard binary.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/yourorg/k8s-dashboard/config"
	"github.com/yourorg/k8s-dashboard/internal/api"
)

func main() {
	// ── Flags ────────────────────────────────────────────────────────────────
	configPath := flag.String("config", "config/config.yaml", "path to config.yaml")

	// Pass -mock to force mock mode even if a real cluster is available.
	// Mock mode is also activated automatically when no kubeconfig is found.
	useMock := flag.Bool("mock", false, "use fake data instead of a real k8s cluster")

	flag.Parse()

	// ── Config ───────────────────────────────────────────────────────────────
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[main] config loaded | port: %d | poll: %s\n",
		cfg.Server.Port, cfg.Server.PollInterval)

	// ── Server ───────────────────────────────────────────────────────────────
	server, err := api.New(cfg, *useMock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create server: %v\n", err)
		os.Exit(1)
	}

	if err := server.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}
