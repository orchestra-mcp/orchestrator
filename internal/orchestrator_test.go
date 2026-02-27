package internal

import (
	"context"
	"testing"
	"time"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/google/uuid"
	"github.com/orchestra-mcp/sdk-go/plugin"
	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestLoadConfig verifies that a plugins.yaml is correctly parsed into Config.
func TestLoadConfig(t *testing.T) {
	yaml := []byte(`
listen_addr: "localhost:50200"
certs_dir: "/tmp/test-certs"
plugins:
  - id: "storage.markdown"
    binary: "./storage-markdown"
    enabled: true
    config:
      workspace: "/home/user/project"
  - id: "tools.features"
    binary: "./tools-features"
    args:
      - "--verbose"
    env:
      LOG_LEVEL: "debug"
    enabled: true
  - id: "disabled.plugin"
    binary: "./disabled"
    enabled: false
`)

	cfg, err := LoadConfigFromBytes(yaml)
	if err != nil {
		t.Fatalf("LoadConfigFromBytes: %v", err)
	}

	if cfg.ListenAddr != "localhost:50200" {
		t.Errorf("ListenAddr: got %q, want %q", cfg.ListenAddr, "localhost:50200")
	}
	if cfg.CertsDir != "/tmp/test-certs" {
		t.Errorf("CertsDir: got %q, want %q", cfg.CertsDir, "/tmp/test-certs")
	}
	if len(cfg.Plugins) != 3 {
		t.Fatalf("Plugins: got %d, want 3", len(cfg.Plugins))
	}

	// Plugin 0: storage.markdown
	p0 := cfg.Plugins[0]
	if p0.ID != "storage.markdown" {
		t.Errorf("Plugin[0].ID: got %q, want %q", p0.ID, "storage.markdown")
	}
	if p0.Binary != "./storage-markdown" {
		t.Errorf("Plugin[0].Binary: got %q, want %q", p0.Binary, "./storage-markdown")
	}
	if !p0.Enabled {
		t.Error("Plugin[0].Enabled: got false, want true")
	}
	if p0.Config["workspace"] != "/home/user/project" {
		t.Errorf("Plugin[0].Config[workspace]: got %q, want %q", p0.Config["workspace"], "/home/user/project")
	}

	// Plugin 1: tools.features
	p1 := cfg.Plugins[1]
	if p1.ID != "tools.features" {
		t.Errorf("Plugin[1].ID: got %q, want %q", p1.ID, "tools.features")
	}
	if len(p1.Args) != 1 || p1.Args[0] != "--verbose" {
		t.Errorf("Plugin[1].Args: got %v, want [--verbose]", p1.Args)
	}
	if p1.Env["LOG_LEVEL"] != "debug" {
		t.Errorf("Plugin[1].Env[LOG_LEVEL]: got %q, want %q", p1.Env["LOG_LEVEL"], "debug")
	}

	// Plugin 2: disabled
	p2 := cfg.Plugins[2]
	if p2.Enabled {
		t.Error("Plugin[2].Enabled: got true, want false")
	}
}

// TestLoadConfigDefaults verifies that defaults are applied for missing fields.
func TestLoadConfigDefaults(t *testing.T) {
	yaml := []byte(`
plugins: []
`)

	cfg, err := LoadConfigFromBytes(yaml)
	if err != nil {
		t.Fatalf("LoadConfigFromBytes: %v", err)
	}

	if cfg.ListenAddr != "localhost:50100" {
		t.Errorf("default ListenAddr: got %q, want %q", cfg.ListenAddr, "localhost:50100")
	}
	if cfg.CertsDir != "~/.orchestra/certs" {
		t.Errorf("default CertsDir: got %q, want %q", cfg.CertsDir, "~/.orchestra/certs")
	}
}

// TestRouter verifies tool and storage routing with mock plugins.
func TestRouter(t *testing.T) {
	tmpDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// -- Start plugin A: provides tool "echo" and storage "markdown" --
	pluginA := startTestPlugin(t, ctx, tmpDir, "plugin-a", func(srv *plugin.Server) {
		echoSchema, _ := structpb.NewStruct(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
			},
		})
		srv.RegisterTool("echo", "Echo back the message", echoSchema,
			func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
				msg := ""
				if req.Arguments != nil {
					if v, ok := req.Arguments.Fields["message"]; ok {
						msg = v.GetStringValue()
					}
				}
				result, _ := structpb.NewStruct(map[string]any{"text": "echo: " + msg})
				return &pluginv1.ToolResponse{Success: true, Result: result}, nil
			})
	})

	// -- Start plugin B: provides tool "greet" --
	pluginB := startTestPlugin(t, ctx, tmpDir, "plugin-b", func(srv *plugin.Server) {
		srv.RegisterTool("greet", "Greet a user", nil,
			func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
				result, _ := structpb.NewStruct(map[string]any{"text": "hello!"})
				return &pluginv1.ToolResponse{Success: true, Result: result}, nil
			})
	})

	// Create router and register plugins.
	router := NewRouter()

	rpA := &RunningPlugin{
		Config: PluginConfig{ID: "plugin-a"},
		Manifest: &pluginv1.PluginManifest{
			Id:              "plugin-a",
			ProvidesTools:   []string{"echo"},
			ProvidesStorage: []string{"markdown"},
		},
		Client: pluginA.client,
	}
	rpB := &RunningPlugin{
		Config: PluginConfig{ID: "plugin-b"},
		Manifest: &pluginv1.PluginManifest{
			Id:            "plugin-b",
			ProvidesTools: []string{"greet"},
		},
		Client: pluginB.client,
	}

	router.RegisterPlugin(rpA)
	router.RegisterPlugin(rpB)

	// Test tool routing: "echo" -> plugin A
	t.Run("RouteToolCall_Echo", func(t *testing.T) {
		args, _ := structpb.NewStruct(map[string]any{"message": "world"})
		resp, err := router.RouteToolCall(ctx, &pluginv1.ToolRequest{
			ToolName:  "echo",
			Arguments: args,
		})
		if err != nil {
			t.Fatalf("RouteToolCall: %v", err)
		}
		if !resp.Success {
			t.Fatalf("expected success, got error: %s", resp.ErrorMessage)
		}
		text := resp.Result.Fields["text"].GetStringValue()
		if text != "echo: world" {
			t.Errorf("result text: got %q, want %q", text, "echo: world")
		}
	})

	// Test tool routing: "greet" -> plugin B
	t.Run("RouteToolCall_Greet", func(t *testing.T) {
		resp, err := router.RouteToolCall(ctx, &pluginv1.ToolRequest{
			ToolName: "greet",
		})
		if err != nil {
			t.Fatalf("RouteToolCall: %v", err)
		}
		if !resp.Success {
			t.Fatalf("expected success, got error: %s", resp.ErrorMessage)
		}
		text := resp.Result.Fields["text"].GetStringValue()
		if text != "hello!" {
			t.Errorf("result text: got %q, want %q", text, "hello!")
		}
	})

	// Test tool not found.
	t.Run("RouteToolCall_NotFound", func(t *testing.T) {
		resp, err := router.RouteToolCall(ctx, &pluginv1.ToolRequest{
			ToolName: "nonexistent",
		})
		if err != nil {
			t.Fatalf("RouteToolCall: %v", err)
		}
		if resp.Success {
			t.Error("expected failure for nonexistent tool")
		}
		if resp.ErrorCode != "tool_not_found" {
			t.Errorf("ErrorCode: got %q, want %q", resp.ErrorCode, "tool_not_found")
		}
	})

	// Test unregister.
	t.Run("UnregisterPlugin", func(t *testing.T) {
		router.UnregisterPlugin("plugin-b")
		resp, err := router.RouteToolCall(ctx, &pluginv1.ToolRequest{
			ToolName: "greet",
		})
		if err != nil {
			t.Fatalf("RouteToolCall: %v", err)
		}
		if resp.Success {
			t.Error("expected failure after unregister")
		}
		if resp.ErrorCode != "tool_not_found" {
			t.Errorf("ErrorCode: got %q, want %q", resp.ErrorCode, "tool_not_found")
		}
	})
}

// TestRouterListAllTools verifies that ListAllTools aggregates tools from
// multiple plugins.
func TestRouterListAllTools(t *testing.T) {
	tmpDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Start plugin with tools "alpha" and "beta".
	pluginA := startTestPlugin(t, ctx, tmpDir, "multi-tools-a", func(srv *plugin.Server) {
		srv.RegisterTool("alpha", "Alpha tool", nil,
			func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
				return &pluginv1.ToolResponse{Success: true}, nil
			})
		srv.RegisterTool("beta", "Beta tool", nil,
			func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
				return &pluginv1.ToolResponse{Success: true}, nil
			})
	})

	// Start plugin with tool "gamma".
	pluginB := startTestPlugin(t, ctx, tmpDir, "multi-tools-b", func(srv *plugin.Server) {
		srv.RegisterTool("gamma", "Gamma tool", nil,
			func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
				return &pluginv1.ToolResponse{Success: true}, nil
			})
	})

	router := NewRouter()
	router.RegisterPlugin(&RunningPlugin{
		Config:   PluginConfig{ID: "multi-tools-a"},
		Manifest: &pluginv1.PluginManifest{Id: "multi-tools-a", ProvidesTools: []string{"alpha", "beta"}},
		Client:   pluginA.client,
	})
	router.RegisterPlugin(&RunningPlugin{
		Config:   PluginConfig{ID: "multi-tools-b"},
		Manifest: &pluginv1.PluginManifest{Id: "multi-tools-b", ProvidesTools: []string{"gamma"}},
		Client:   pluginB.client,
	})

	tools, err := router.ListAllTools(ctx)
	if err != nil {
		t.Fatalf("ListAllTools: %v", err)
	}

	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}

	// Build a map for easy lookup.
	toolMap := make(map[string]*pluginv1.ToolDefinition)
	for _, td := range tools {
		toolMap[td.Name] = td
	}

	for _, name := range []string{"alpha", "beta", "gamma"} {
		if _, ok := toolMap[name]; !ok {
			t.Errorf("expected tool %q in aggregated list", name)
		}
	}
}

// TestOrchestratorServerDispatch tests the orchestrator's QUIC server by
// sending requests to it and verifying routing.
func TestOrchestratorServerDispatch(t *testing.T) {
	tmpDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Start a test plugin that provides tool "ping".
	testPlugin := startTestPlugin(t, ctx, tmpDir, "dispatch-plugin", func(srv *plugin.Server) {
		srv.RegisterTool("ping", "Ping tool", nil,
			func(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
				result, _ := structpb.NewStruct(map[string]any{"pong": true})
				return &pluginv1.ToolResponse{Success: true, Result: result}, nil
			})
	})

	// Set up router.
	router := NewRouter()
	router.RegisterPlugin(&RunningPlugin{
		Config:   PluginConfig{ID: "dispatch-plugin"},
		Manifest: &pluginv1.PluginManifest{Id: "dispatch-plugin", ProvidesTools: []string{"ping"}},
		Client:   testPlugin.client,
	})

	// Start the orchestrator server.
	serverTLS, err := plugin.ServerTLSConfig(tmpDir, "orch-dispatch-server")
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}

	orchServer := NewOrchestratorServer("localhost:0", serverTLS, router)

	serverReady := make(chan string, 1)
	go func() {
		// We need to start the server and get its actual address.
		listener, err := quic.ListenAddr("localhost:0", serverTLS, &quic.Config{})
		if err != nil {
			t.Errorf("listen: %v", err)
			return
		}
		orchServer.listener = listener
		serverReady <- listener.Addr().String()

		go func() {
			<-ctx.Done()
			listener.Close()
		}()

		for {
			conn, err := listener.Accept(ctx)
			if err != nil {
				return
			}
			go orchServer.handleConnection(ctx, conn)
		}
	}()

	// Wait for server address.
	var serverAddr string
	select {
	case serverAddr = <-serverReady:
	case <-time.After(5 * time.Second):
		t.Fatal("orchestrator server did not start in time")
	}

	time.Sleep(100 * time.Millisecond)

	// Connect as a client.
	clientTLS, err := plugin.ClientTLSConfig(tmpDir, "orch-dispatch-client")
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}

	client, err := plugin.NewOrchestratorClient(ctx, serverAddr, clientTLS)
	if err != nil {
		t.Fatalf("NewOrchestratorClient: %v", err)
	}
	defer client.Close()

	// Test: Health check via orchestrator server.
	t.Run("Health", func(t *testing.T) {
		resp, err := client.Send(ctx, &pluginv1.PluginRequest{
			RequestId: uuid.New().String(),
			Request: &pluginv1.PluginRequest_Health{
				Health: &pluginv1.HealthRequest{},
			},
		})
		if err != nil {
			t.Fatalf("Send Health: %v", err)
		}
		h := resp.GetHealth()
		if h == nil {
			t.Fatal("expected health response")
		}
		if h.Status != pluginv1.HealthResult_STATUS_HEALTHY {
			t.Errorf("health status: got %v, want HEALTHY", h.Status)
		}
	})

	// Test: Tool call routed through orchestrator server.
	t.Run("ToolCallViaServer", func(t *testing.T) {
		resp, err := client.Send(ctx, &pluginv1.PluginRequest{
			RequestId: uuid.New().String(),
			Request: &pluginv1.PluginRequest_ToolCall{
				ToolCall: &pluginv1.ToolRequest{
					ToolName: "ping",
				},
			},
		})
		if err != nil {
			t.Fatalf("Send ToolCall: %v", err)
		}
		tc := resp.GetToolCall()
		if tc == nil {
			t.Fatal("expected tool_call response")
		}
		if !tc.Success {
			t.Errorf("expected success, got error: %s", tc.ErrorMessage)
		}
		if tc.Result == nil || !tc.Result.Fields["pong"].GetBoolValue() {
			t.Error("expected pong=true in result")
		}
	})

	// Test: ListTools via orchestrator server.
	t.Run("ListToolsViaServer", func(t *testing.T) {
		resp, err := client.Send(ctx, &pluginv1.PluginRequest{
			RequestId: uuid.New().String(),
			Request: &pluginv1.PluginRequest_ListTools{
				ListTools: &pluginv1.ListToolsRequest{},
			},
		})
		if err != nil {
			t.Fatalf("Send ListTools: %v", err)
		}
		lt := resp.GetListTools()
		if lt == nil {
			t.Fatal("expected list_tools response")
		}
		if len(lt.Tools) != 1 {
			t.Fatalf("expected 1 tool, got %d", len(lt.Tools))
		}
		if lt.Tools[0].Name != "ping" {
			t.Errorf("tool name: got %q, want %q", lt.Tools[0].Name, "ping")
		}
	})

	cancel()
}

// --- test helpers ---

// testPluginHandle holds a running test plugin server and a client connected
// to it.
type testPluginHandle struct {
	server *plugin.Server
	client *plugin.OrchestratorClient
	addr   string
}

// startTestPlugin creates a QUIC server with registered tools, starts it, and
// returns a client connected to it. The server is stopped when the context is
// cancelled.
func startTestPlugin(t *testing.T, ctx context.Context, certsDir string, name string, setup func(srv *plugin.Server)) *testPluginHandle {
	t.Helper()

	serverTLS, err := plugin.ServerTLSConfig(certsDir, name+"-server")
	if err != nil {
		t.Fatalf("ServerTLSConfig for %s: %v", name, err)
	}

	srv := plugin.NewServer("localhost:0", serverTLS)
	setup(srv)

	// Listen on ephemeral port to discover the actual address.
	listener, err := quic.ListenAddr("localhost:0", serverTLS, &quic.Config{})
	if err != nil {
		t.Fatalf("listen for %s: %v", name, err)
	}
	actualAddr := listener.Addr().String()
	listener.Close()

	srv = plugin.NewServer(actualAddr, serverTLS)
	setup(srv)

	go func() {
		_ = srv.ListenAndServe(ctx)
	}()

	// Give the server time to start.
	time.Sleep(150 * time.Millisecond)

	clientTLS, err := plugin.ClientTLSConfig(certsDir, name+"-client")
	if err != nil {
		t.Fatalf("ClientTLSConfig for %s: %v", name, err)
	}

	client, err := plugin.NewOrchestratorClient(ctx, actualAddr, clientTLS)
	if err != nil {
		t.Fatalf("connect to %s at %s: %v", name, actualAddr, err)
	}

	t.Cleanup(func() {
		client.Close()
	})

	return &testPluginHandle{
		server: srv,
		client: client,
		addr:   actualAddr,
	}
}
