package internal

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/google/uuid"
	"github.com/orchestra-mcp/sdk-go/plugin"
)

// RunningPlugin holds the runtime state for a single plugin that has been
// started, registered, and booted by the orchestrator.
type RunningPlugin struct {
	Config   PluginConfig
	Manifest *pluginv1.PluginManifest
	Client   *plugin.OrchestratorClient
	Process  *os.Process
	Addr     string
	cmd      *exec.Cmd
}

// Loader manages starting and stopping plugin binaries. It maintains a
// registry of all running plugins keyed by their ID.
type Loader struct {
	certsDir string

	mu      sync.Mutex
	plugins map[string]*RunningPlugin
}

// NewLoader creates a new Loader that generates mTLS certificates in the given
// directory.
func NewLoader(certsDir string) *Loader {
	return &Loader{
		certsDir: certsDir,
		plugins:  make(map[string]*RunningPlugin),
	}
}

// StartPlugin launches a plugin binary, waits for it to print "READY <addr>"
// on stderr, connects to it via QUIC, sends Register and Boot, and returns
// the RunningPlugin.
func (l *Loader) StartPlugin(ctx context.Context, cfg PluginConfig, orchestratorAddr string) (*RunningPlugin, error) {
	// Build command arguments.
	args := make([]string, 0, len(cfg.Args)+6)
	args = append(args, cfg.Args...)
	args = append(args,
		"--orchestrator-addr="+orchestratorAddr,
		"--listen-addr=localhost:0",
		"--certs-dir="+l.certsDir,
	)

	cmd := exec.CommandContext(ctx, cfg.Binary, args...)

	// Set environment variables from config.
	if len(cfg.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range cfg.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	// Capture stderr to read the READY line.
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("create stderr pipe for %q: %w", cfg.ID, err)
	}

	// Start the process.
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start plugin %q (%s): %w", cfg.ID, cfg.Binary, err)
	}

	log.Printf("started plugin %q (pid=%d)", cfg.ID, cmd.Process.Pid)

	// Read stderr looking for "READY <addr>" with a timeout.
	addrCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "READY ") {
				addr := strings.TrimPrefix(line, "READY ")
				addrCh <- strings.TrimSpace(addr)
				return
			}
			// Forward other stderr lines as log output.
			log.Printf("[%s] %s", cfg.ID, line)
		}
		if err := scanner.Err(); err != nil {
			errCh <- fmt.Errorf("read stderr for %q: %w", cfg.ID, err)
		} else {
			errCh <- fmt.Errorf("plugin %q exited without printing READY", cfg.ID)
		}
	}()

	// Wait for READY or timeout.
	var pluginAddr string
	select {
	case pluginAddr = <-addrCh:
		log.Printf("plugin %q ready at %s", cfg.ID, pluginAddr)
	case err := <-errCh:
		_ = cmd.Process.Kill()
		return nil, err
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("plugin %q did not become ready within 10 seconds", cfg.ID)
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return nil, ctx.Err()
	}

	// Connect to the plugin via QUIC (mTLS).
	clientTLS, err := plugin.ClientTLSConfig(l.certsDir, "orchestrator-to-"+cfg.ID)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("client TLS config for %q: %w", cfg.ID, err)
	}

	client, err := plugin.NewOrchestratorClient(ctx, pluginAddr, clientTLS)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("connect to plugin %q at %s: %w", cfg.ID, pluginAddr, err)
	}

	// Send Register.
	manifest := &pluginv1.PluginManifest{
		Id: cfg.ID,
	}
	regResp, err := client.Send(ctx, &pluginv1.PluginRequest{
		RequestId: uuid.New().String(),
		Request: &pluginv1.PluginRequest_Register{
			Register: manifest,
		},
	})
	if err != nil {
		client.Close()
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("register plugin %q: %w", cfg.ID, err)
	}
	reg := regResp.GetRegister()
	if reg == nil || !reg.GetAccepted() {
		reason := ""
		if reg != nil {
			reason = reg.GetRejectReason()
		}
		client.Close()
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("plugin %q rejected registration: %s", cfg.ID, reason)
	}

	// Send Boot with config.
	bootResp, err := client.Send(ctx, &pluginv1.PluginRequest{
		RequestId: uuid.New().String(),
		Request: &pluginv1.PluginRequest_Boot{
			Boot: &pluginv1.BootRequest{
				Config: cfg.Config,
			},
		},
	})
	if err != nil {
		client.Close()
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("boot plugin %q: %w", cfg.ID, err)
	}
	boot := bootResp.GetBoot()
	if boot == nil || !boot.GetReady() {
		bootErr := ""
		if boot != nil {
			bootErr = boot.GetError()
		}
		client.Close()
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("plugin %q failed to boot: %s", cfg.ID, bootErr)
	}

	// Populate storage capabilities from config.
	if len(cfg.ProvidesStorage) > 0 {
		manifest.ProvidesStorage = cfg.ProvidesStorage
	}

	// Query the plugin for its actual manifest via ListTools to populate routes.
	// The plugin's own registration response does not include the full manifest,
	// so we rebuild it from what we know plus a ListTools query.
	toolsResp, err := client.Send(ctx, &pluginv1.PluginRequest{
		RequestId: uuid.New().String(),
		Request: &pluginv1.PluginRequest_ListTools{
			ListTools: &pluginv1.ListToolsRequest{},
		},
	})
	if err == nil {
		lt := toolsResp.GetListTools()
		if lt != nil {
			toolNames := make([]string, len(lt.GetTools()))
			for i, t := range lt.GetTools() {
				toolNames[i] = t.GetName()
			}
			manifest.ProvidesTools = toolNames
		}
	}

	rp := &RunningPlugin{
		Config:   cfg,
		Manifest: manifest,
		Client:   client,
		Process:  cmd.Process,
		Addr:     pluginAddr,
		cmd:      cmd,
	}

	l.mu.Lock()
	l.plugins[cfg.ID] = rp
	l.mu.Unlock()

	log.Printf("plugin %q registered and booted", cfg.ID)
	return rp, nil
}

// StopPlugin gracefully shuts down a single plugin by sending Shutdown, closing
// the client connection, and killing the process.
func (l *Loader) StopPlugin(id string) error {
	l.mu.Lock()
	rp, ok := l.plugins[id]
	if ok {
		delete(l.plugins, id)
	}
	l.mu.Unlock()

	if !ok {
		return fmt.Errorf("plugin %q not found", id)
	}

	return stopRunningPlugin(rp)
}

// StopAll gracefully shuts down all running plugins.
func (l *Loader) StopAll() error {
	l.mu.Lock()
	plugins := make(map[string]*RunningPlugin, len(l.plugins))
	for k, v := range l.plugins {
		plugins[k] = v
	}
	l.plugins = make(map[string]*RunningPlugin)
	l.mu.Unlock()

	var firstErr error
	for id, rp := range plugins {
		if err := stopRunningPlugin(rp); err != nil {
			log.Printf("error stopping plugin %q: %v", id, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr
}

// stopRunningPlugin sends a Shutdown request, closes the QUIC connection, and
// kills the plugin process.
func stopRunningPlugin(rp *RunningPlugin) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send Shutdown.
	if rp.Client != nil {
		_, err := rp.Client.Send(ctx, &pluginv1.PluginRequest{
			RequestId: uuid.New().String(),
			Request: &pluginv1.PluginRequest_Shutdown{
				Shutdown: &pluginv1.ShutdownRequest{},
			},
		})
		if err != nil {
			log.Printf("shutdown request to %q failed: %v", rp.Config.ID, err)
		}
		rp.Client.Close()
	}

	// Kill the process.
	if rp.Process != nil {
		_ = rp.Process.Kill()
	}

	log.Printf("stopped plugin %q", rp.Config.ID)
	return nil
}
