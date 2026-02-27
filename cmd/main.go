package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/orchestra-mcp/orchestrator/internal"
)

func main() {
	configPath := flag.String("config", "plugins.yaml", "Path to plugins.yaml config file")
	listenAddr := flag.String("listen-addr", "", "Override listen address (default from config or localhost:50100)")
	certsDir := flag.String("certs-dir", "", "Override mTLS certificates directory")
	flag.Parse()

	// Load configuration.
	cfg, err := internal.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// Apply CLI overrides.
	if *listenAddr != "" {
		cfg.ListenAddr = *listenAddr
	}
	if *certsDir != "" {
		cfg.CertsDir = *certsDir
	}

	// Create orchestrator.
	orch := internal.NewOrchestrator(cfg)

	// Create cancellable context that responds to OS signals.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("received signal %v, shutting down", sig)
		cancel()
	}()

	// Start orchestrator (blocks until context cancelled).
	if err := orch.Start(ctx); err != nil {
		log.Printf("orchestrator error: %v", err)
	}

	// Graceful shutdown.
	if err := orch.Stop(); err != nil {
		log.Printf("shutdown error: %v", err)
	}

	log.Printf("orchestrator stopped")
}
