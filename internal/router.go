package internal

import (
	"context"
	"fmt"
	"sync"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/google/uuid"
)

// Router maintains routing tables that map tool names and storage types to the
// plugins that provide them. It dispatches tool calls and storage operations to
// the correct plugin via its QUIC client connection.
type Router struct {
	mu            sync.RWMutex
	toolRoutes    map[string]*RunningPlugin  // toolName -> plugin
	storageRoutes map[string]*RunningPlugin  // storageType -> plugin
	plugins       map[string]*RunningPlugin  // pluginID -> plugin
}

// NewRouter creates a new empty Router.
func NewRouter() *Router {
	return &Router{
		toolRoutes:    make(map[string]*RunningPlugin),
		storageRoutes: make(map[string]*RunningPlugin),
		plugins:       make(map[string]*RunningPlugin),
	}
}

// RegisterPlugin extracts the provides_tools and provides_storage declarations
// from a plugin's manifest and adds them to the routing tables.
func (r *Router) RegisterPlugin(rp *RunningPlugin) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.plugins[rp.Config.ID] = rp

	if rp.Manifest != nil {
		for _, tool := range rp.Manifest.GetProvidesTools() {
			r.toolRoutes[tool] = rp
		}
		for _, st := range rp.Manifest.GetProvidesStorage() {
			r.storageRoutes[st] = rp
		}
	}
}

// UnregisterPlugin removes all routes for the given plugin ID.
func (r *Router) UnregisterPlugin(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	rp, ok := r.plugins[id]
	if !ok {
		return
	}

	if rp.Manifest != nil {
		for _, tool := range rp.Manifest.GetProvidesTools() {
			if r.toolRoutes[tool] == rp {
				delete(r.toolRoutes, tool)
			}
		}
		for _, st := range rp.Manifest.GetProvidesStorage() {
			if r.storageRoutes[st] == rp {
				delete(r.storageRoutes, st)
			}
		}
	}

	delete(r.plugins, id)
}

// RouteToolCall dispatches a tool invocation to the plugin that provides it.
func (r *Router) RouteToolCall(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
	r.mu.RLock()
	rp, ok := r.toolRoutes[req.GetToolName()]
	r.mu.RUnlock()

	if !ok {
		return &pluginv1.ToolResponse{
			Success:      false,
			ErrorCode:    "tool_not_found",
			ErrorMessage: fmt.Sprintf("no plugin provides tool %q", req.GetToolName()),
		}, nil
	}

	resp, err := rp.Client.Send(ctx, &pluginv1.PluginRequest{
		RequestId: uuid.New().String(),
		Request: &pluginv1.PluginRequest_ToolCall{
			ToolCall: req,
		},
	})
	if err != nil {
		return &pluginv1.ToolResponse{
			Success:      false,
			ErrorCode:    "routing_error",
			ErrorMessage: fmt.Sprintf("failed to route tool call to plugin %q: %v", rp.Config.ID, err),
		}, nil
	}

	tc := resp.GetToolCall()
	if tc == nil {
		return &pluginv1.ToolResponse{
			Success:      false,
			ErrorCode:    "invalid_response",
			ErrorMessage: "plugin returned non-tool-call response",
		}, nil
	}

	return tc, nil
}

// RouteStorageRead dispatches a storage read to the plugin that provides the
// requested storage type.
func (r *Router) RouteStorageRead(ctx context.Context, req *pluginv1.StorageReadRequest) (*pluginv1.StorageReadResponse, error) {
	rp, err := r.findStoragePlugin(req.GetStorageType())
	if err != nil {
		return nil, err
	}

	resp, err := rp.Client.Send(ctx, &pluginv1.PluginRequest{
		RequestId: uuid.New().String(),
		Request: &pluginv1.PluginRequest_StorageRead{
			StorageRead: req,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("route storage_read to %q: %w", rp.Config.ID, err)
	}

	sr := resp.GetStorageRead()
	if sr == nil {
		return nil, fmt.Errorf("plugin %q returned non-storage-read response", rp.Config.ID)
	}

	return sr, nil
}

// RouteStorageWrite dispatches a storage write to the plugin that provides the
// requested storage type.
func (r *Router) RouteStorageWrite(ctx context.Context, req *pluginv1.StorageWriteRequest) (*pluginv1.StorageWriteResponse, error) {
	rp, err := r.findStoragePlugin(req.GetStorageType())
	if err != nil {
		return nil, err
	}

	resp, err := rp.Client.Send(ctx, &pluginv1.PluginRequest{
		RequestId: uuid.New().String(),
		Request: &pluginv1.PluginRequest_StorageWrite{
			StorageWrite: req,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("route storage_write to %q: %w", rp.Config.ID, err)
	}

	sw := resp.GetStorageWrite()
	if sw == nil {
		return nil, fmt.Errorf("plugin %q returned non-storage-write response", rp.Config.ID)
	}

	return sw, nil
}

// RouteStorageDelete dispatches a storage delete to the plugin that provides
// the requested storage type.
func (r *Router) RouteStorageDelete(ctx context.Context, req *pluginv1.StorageDeleteRequest) (*pluginv1.StorageDeleteResponse, error) {
	rp, err := r.findStoragePlugin(req.GetStorageType())
	if err != nil {
		return nil, err
	}

	resp, err := rp.Client.Send(ctx, &pluginv1.PluginRequest{
		RequestId: uuid.New().String(),
		Request: &pluginv1.PluginRequest_StorageDelete{
			StorageDelete: req,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("route storage_delete to %q: %w", rp.Config.ID, err)
	}

	sd := resp.GetStorageDelete()
	if sd == nil {
		return nil, fmt.Errorf("plugin %q returned non-storage-delete response", rp.Config.ID)
	}

	return sd, nil
}

// RouteStorageList dispatches a storage list to the plugin that provides the
// requested storage type.
func (r *Router) RouteStorageList(ctx context.Context, req *pluginv1.StorageListRequest) (*pluginv1.StorageListResponse, error) {
	rp, err := r.findStoragePlugin(req.GetStorageType())
	if err != nil {
		return nil, err
	}

	resp, err := rp.Client.Send(ctx, &pluginv1.PluginRequest{
		RequestId: uuid.New().String(),
		Request: &pluginv1.PluginRequest_StorageList{
			StorageList: req,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("route storage_list to %q: %w", rp.Config.ID, err)
	}

	sl := resp.GetStorageList()
	if sl == nil {
		return nil, fmt.Errorf("plugin %q returned non-storage-list response", rp.Config.ID)
	}

	return sl, nil
}

// ListAllTools queries every registered plugin for its tools and returns an
// aggregated list of all tool definitions.
func (r *Router) ListAllTools(ctx context.Context) ([]*pluginv1.ToolDefinition, error) {
	r.mu.RLock()
	pluginsCopy := make([]*RunningPlugin, 0, len(r.plugins))
	for _, rp := range r.plugins {
		pluginsCopy = append(pluginsCopy, rp)
	}
	r.mu.RUnlock()

	var allTools []*pluginv1.ToolDefinition
	for _, rp := range pluginsCopy {
		resp, err := rp.Client.Send(ctx, &pluginv1.PluginRequest{
			RequestId: uuid.New().String(),
			Request: &pluginv1.PluginRequest_ListTools{
				ListTools: &pluginv1.ListToolsRequest{},
			},
		})
		if err != nil {
			// Log the error but continue collecting from other plugins.
			continue
		}
		lt := resp.GetListTools()
		if lt != nil {
			allTools = append(allTools, lt.GetTools()...)
		}
	}

	return allTools, nil
}

// findStoragePlugin looks up the plugin providing the given storage type.
func (r *Router) findStoragePlugin(storageType string) (*RunningPlugin, error) {
	if storageType == "" {
		storageType = "markdown" // default storage type
	}

	r.mu.RLock()
	rp, ok := r.storageRoutes[storageType]
	r.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("no plugin provides storage type %q", storageType)
	}

	return rp, nil
}
