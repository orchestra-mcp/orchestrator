package internal

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/orchestra-mcp/sdk-go/plugin"
)

// Orchestrator is the central hub that loads plugins, manages their lifecycle,
// and routes messages between them. It runs a QUIC server that plugins connect
// to for making callback requests (tool calls, storage operations), and it
// connects to each plugin as a client to send lifecycle and tool/storage
// requests.
type Orchestrator struct {
	config   *Config
	server   *OrchestratorServer
	router   *Router
	loader   *Loader
	certsDir string
}

// NewOrchestrator creates a new Orchestrator from the given configuration.
func NewOrchestrator(config *Config) *Orchestrator {
	certsDir := plugin.ResolveCertsDir(config.CertsDir)

	return &Orchestrator{
		config:   config,
		router:   NewRouter(),
		loader:   NewLoader(certsDir),
		certsDir: certsDir,
	}
}

// Start initializes mTLS certificates, starts the QUIC server, launches all
// enabled plugins, and blocks until the context is cancelled.
func (o *Orchestrator) Start(ctx context.Context) error {
	// Step 1: Ensure mTLS certs exist.
	serverTLS, err := plugin.ServerTLSConfig(o.certsDir, "orchestrator")
	if err != nil {
		return fmt.Errorf("orchestrator TLS config: %w", err)
	}

	// Step 2: Start the QUIC server for plugin callbacks.
	o.server = NewOrchestratorServer(o.config.ListenAddr, serverTLS, o.router)

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- o.server.ListenAndServe(ctx)
	}()

	// Also start a plain TCP server on port+1 for clients (e.g. Swift/macOS)
	// that cannot open raw QUIC streams via Network.framework.
	tcpAddr := o.config.TCPAddr
	if tcpAddr == "" {
		tcpAddr = "localhost:50101"
	}
	go func() {
		if err := o.server.ListenAndServeTCP(ctx, tcpAddr); err != nil {
			log.Printf("TCP server error: %v", err)
		}
	}()

	// Give the server a moment to bind.
	time.Sleep(100 * time.Millisecond)
	actualAddr := o.server.Addr()
	log.Printf("orchestrator listening on %s", actualAddr)

	// Step 3: Launch all enabled plugins.
	for _, pcfg := range o.config.Plugins {
		if !pcfg.Enabled {
			log.Printf("skipping disabled plugin %q", pcfg.ID)
			continue
		}

		rp, err := o.loader.StartPlugin(ctx, pcfg, actualAddr)
		if err != nil {
			log.Printf("failed to start plugin %q: %v", pcfg.ID, err)
			continue
		}

		o.router.RegisterPlugin(rp)
		o.router.AutoSubscribeFromManifest(rp)
		log.Printf("plugin %q registered with router", pcfg.ID)
	}

	// Step 4: Block until context cancelled or server error.
	select {
	case <-ctx.Done():
		log.Printf("orchestrator shutting down")
	case err := <-serverErr:
		if err != nil {
			return fmt.Errorf("server error: %w", err)
		}
	}

	return nil
}

// Stop gracefully shuts down all plugins and closes the server.
func (o *Orchestrator) Stop() error {
	log.Printf("stopping all plugins")
	if err := o.loader.StopAll(); err != nil {
		log.Printf("error stopping plugins: %v", err)
	}
	return nil
}

// Router returns the orchestrator's message router. This is useful for testing
// and for direct access to routing functionality.
func (o *Orchestrator) Router() *Router {
	return o.router
}

// Server returns the orchestrator's QUIC server. Only valid after Start.
func (o *Orchestrator) Server() *OrchestratorServer {
	return o.server
}
