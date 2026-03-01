package internal

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// eventSubscription tracks a plugin's subscription to an event topic.
type eventSubscription struct {
	id      string
	topic   string
	filters map[string]string
	plugin  *RunningPlugin
}

// Router maintains routing tables that map tool names and storage types to the
// plugins that provide them. It dispatches tool calls and storage operations to
// the correct plugin via its QUIC client connection.
type Router struct {
	mu            sync.RWMutex
	toolRoutes    map[string]*RunningPlugin             // toolName -> plugin
	promptRoutes  map[string]*RunningPlugin             // promptName -> plugin
	storageRoutes map[string]*RunningPlugin             // storageType -> plugin
	aiRoutes      map[string]map[string]*RunningPlugin  // provider -> toolName -> plugin
	plugins       map[string]*RunningPlugin             // pluginID -> plugin
	eventSubs     map[string]*eventSubscription          // subscriptionID -> subscription
}

// NewRouter creates a new empty Router.
func NewRouter() *Router {
	return &Router{
		toolRoutes:    make(map[string]*RunningPlugin),
		promptRoutes:  make(map[string]*RunningPlugin),
		storageRoutes: make(map[string]*RunningPlugin),
		aiRoutes:      make(map[string]map[string]*RunningPlugin),
		plugins:       make(map[string]*RunningPlugin),
		eventSubs:     make(map[string]*eventSubscription),
	}
}

// RegisterPlugin extracts the provides_tools, provides_storage, and provides_ai
// declarations from a plugin's manifest and adds them to the routing tables.
// AI bridge plugins (provides_ai) have their tools indexed by provider so that
// RouteToolCall can dispatch by provider when ToolRequest.provider is set.
func (r *Router) RegisterPlugin(rp *RunningPlugin) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.plugins[rp.Config.ID] = rp

	if rp.Manifest != nil {
		for _, tool := range rp.Manifest.GetProvidesTools() {
			r.toolRoutes[tool] = rp
		}
		for _, prompt := range rp.Manifest.GetProvidesPrompts() {
			r.promptRoutes[prompt] = rp
		}
		for _, st := range rp.Manifest.GetProvidesStorage() {
			r.storageRoutes[st] = rp
		}
		// Index AI bridge tools by provider.
		for _, provider := range rp.Manifest.GetProvidesAi() {
			if r.aiRoutes[provider] == nil {
				r.aiRoutes[provider] = make(map[string]*RunningPlugin)
			}
			for _, tool := range rp.Manifest.GetProvidesTools() {
				r.aiRoutes[provider][tool] = rp
			}
			log.Printf("plugin %q registered AI provider %q", rp.Config.ID, provider)
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
		for _, prompt := range rp.Manifest.GetProvidesPrompts() {
			if r.promptRoutes[prompt] == rp {
				delete(r.promptRoutes, prompt)
			}
		}
		for _, st := range rp.Manifest.GetProvidesStorage() {
			if r.storageRoutes[st] == rp {
				delete(r.storageRoutes, st)
			}
		}
		for _, provider := range rp.Manifest.GetProvidesAi() {
			if providerTools, ok := r.aiRoutes[provider]; ok {
				for tool, p := range providerTools {
					if p == rp {
						delete(providerTools, tool)
					}
				}
				if len(providerTools) == 0 {
					delete(r.aiRoutes, provider)
				}
			}
		}
	}

	delete(r.plugins, id)
}

// RouteToolCall dispatches a tool invocation to the plugin that provides it.
// When ToolRequest.provider is set, it first looks in the AI routing table to
// find the bridge plugin for that provider. This allows the same tool name
// (e.g., "ai_prompt") to be served by different plugins depending on the
// provider (claude, openai, gemini, ollama, etc.).
func (r *Router) RouteToolCall(ctx context.Context, req *pluginv1.ToolRequest) (*pluginv1.ToolResponse, error) {
	r.mu.RLock()
	rp := r.findToolPlugin(req.GetToolName(), req.GetProvider())
	r.mu.RUnlock()

	if rp == nil {
		msg := fmt.Sprintf("no plugin provides tool %q", req.GetToolName())
		if req.GetProvider() != "" {
			msg = fmt.Sprintf("no plugin provides tool %q for provider %q", req.GetToolName(), req.GetProvider())
		}
		return &pluginv1.ToolResponse{
			Success:      false,
			ErrorCode:    "tool_not_found",
			ErrorMessage: msg,
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

// providerAliases maps OpenAI-compatible providers to "openai" so they route
// to bridge-openai when no dedicated bridge plugin is registered.
var providerAliases = map[string]string{
	"grok":       "openai",
	"perplexity": "openai",
	"deepseek":   "openai",
	"qwen":       "openai",
	"kimi":       "openai",
}

// findToolPlugin looks up the plugin for a tool, preferring provider-specific
// AI routes when a provider is specified. For OpenAI-compatible providers
// (deepseek, qwen, kimi, grok, perplexity), falls back to the "openai" bridge
// when no dedicated bridge exists. Must be called with r.mu held.
func (r *Router) findToolPlugin(toolName, provider string) *RunningPlugin {
	// If provider is specified, look in AI routes first.
	if provider != "" {
		if providerTools, ok := r.aiRoutes[provider]; ok {
			if rp, ok := providerTools[toolName]; ok {
				return rp
			}
		}
		// Fallback: check if this provider aliases to another (e.g., deepseek -> openai).
		if alias, ok := providerAliases[provider]; ok {
			if providerTools, ok := r.aiRoutes[alias]; ok {
				if rp, ok := providerTools[toolName]; ok {
					return rp
				}
			}
		}
	}
	// Fallback: if a provider was specified but not found in AI routes, try "claude"
	// as the default AI bridge before falling through to generic tool routes.
	// This ensures chat requests default to Claude when the provider field is set
	// but the specific provider isn't registered.
	if provider != "" {
		if providerTools, ok := r.aiRoutes["claude"]; ok {
			if rp, ok := providerTools[toolName]; ok {
				return rp
			}
		}
	}
	// Fallback to generic tool routes.
	if rp, ok := r.toolRoutes[toolName]; ok {
		return rp
	}
	return nil
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

// ListAllPrompts queries every registered plugin for its prompts and returns an
// aggregated list of all prompt definitions.
func (r *Router) ListAllPrompts(ctx context.Context) ([]*pluginv1.PromptDefinition, error) {
	r.mu.RLock()
	pluginsCopy := make([]*RunningPlugin, 0, len(r.plugins))
	for _, rp := range r.plugins {
		pluginsCopy = append(pluginsCopy, rp)
	}
	r.mu.RUnlock()

	var allPrompts []*pluginv1.PromptDefinition
	for _, rp := range pluginsCopy {
		resp, err := rp.Client.Send(ctx, &pluginv1.PluginRequest{
			RequestId: uuid.New().String(),
			Request: &pluginv1.PluginRequest_ListPrompts{
				ListPrompts: &pluginv1.ListPromptsRequest{},
			},
		})
		if err != nil {
			continue
		}
		lp := resp.GetListPrompts()
		if lp != nil {
			allPrompts = append(allPrompts, lp.GetPrompts()...)
		}
	}

	return allPrompts, nil
}

// RoutePromptGet dispatches a prompt get request to the plugin that provides it.
func (r *Router) RoutePromptGet(ctx context.Context, req *pluginv1.PromptGetRequest) (*pluginv1.PromptGetResponse, error) {
	r.mu.RLock()
	rp, ok := r.promptRoutes[req.GetPromptName()]
	r.mu.RUnlock()

	if !ok {
		return &pluginv1.PromptGetResponse{
			Description: fmt.Sprintf("no plugin provides prompt %q", req.GetPromptName()),
		}, nil
	}

	resp, err := rp.Client.Send(ctx, &pluginv1.PluginRequest{
		RequestId: uuid.New().String(),
		Request: &pluginv1.PluginRequest_PromptGet{
			PromptGet: req,
		},
	})
	if err != nil {
		return &pluginv1.PromptGetResponse{
			Description: fmt.Sprintf("failed to route prompt get to plugin %q: %v", rp.Config.ID, err),
		}, nil
	}

	pg := resp.GetPromptGet()
	if pg == nil {
		return &pluginv1.PromptGetResponse{
			Description: "plugin returned non-prompt-get response",
		}, nil
	}

	return pg, nil
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

// ================================================================
// Events (Pub/Sub)
// ================================================================

// AddSubscription registers an event subscription from a plugin.
func (r *Router) AddSubscription(sub *pluginv1.Subscribe, rp *RunningPlugin) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eventSubs[sub.SubscriptionId] = &eventSubscription{
		id:      sub.SubscriptionId,
		topic:   sub.Topic,
		filters: sub.Filters,
		plugin:  rp,
	}
}

// RemoveSubscription removes an event subscription by ID.
func (r *Router) RemoveSubscription(subscriptionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.eventSubs, subscriptionID)
}

// Publish fans out an event to all subscribers whose topic matches. Filter
// matching requires all filter keys to be present and equal in the payload.
// Delivery is asynchronous — each subscriber gets the event on a separate
// goroutine via its QUIC client connection.
func (r *Router) Publish(ctx context.Context, pub *pluginv1.Publish) {
	r.mu.RLock()
	var matches []*eventSubscription
	for _, sub := range r.eventSubs {
		if sub.topic != pub.Topic {
			continue
		}
		if !matchFilters(sub.filters, pub.Payload) {
			continue
		}
		matches = append(matches, sub)
	}
	r.mu.RUnlock()

	now := timestamppb.New(time.Now())
	for _, sub := range matches {
		sub := sub // capture for goroutine
		go func() {
			delivery := &pluginv1.EventDelivery{
				SubscriptionId: sub.id,
				Topic:          pub.Topic,
				EventType:      pub.EventType,
				Payload:        pub.Payload,
				SourcePlugin:   pub.SourcePlugin,
				Timestamp:      now,
			}
			_, err := sub.plugin.Client.Send(ctx, &pluginv1.PluginRequest{
				RequestId: uuid.New().String(),
				Request: &pluginv1.PluginRequest_Publish{
					Publish: &pluginv1.Publish{
						Topic:        delivery.Topic,
						EventType:    delivery.EventType,
						Payload:      delivery.Payload,
						SourcePlugin: delivery.SourcePlugin,
					},
				},
			})
			if err != nil {
				log.Printf("event delivery to plugin %q (sub %s): %v", sub.plugin.Config.ID, sub.id, err)
			}
		}()
	}
}

// AutoSubscribeFromManifest registers subscriptions for events that a plugin
// declares in its manifest's needs_events list.
func (r *Router) AutoSubscribeFromManifest(rp *RunningPlugin) {
	if rp.Manifest == nil {
		return
	}
	for _, topic := range rp.Manifest.GetNeedsEvents() {
		subID := uuid.New().String()
		r.AddSubscription(&pluginv1.Subscribe{
			SubscriptionId: subID,
			Topic:          topic,
		}, rp)
		log.Printf("auto-subscribed plugin %q to topic %q (sub %s)", rp.Config.ID, topic, subID)
	}
}

// matchFilters checks if all filter key=value pairs match fields in the payload.
func matchFilters(filters map[string]string, payload *structpb.Struct) bool {
	if len(filters) == 0 {
		return true
	}
	if payload == nil {
		return false
	}
	for k, v := range filters {
		field, ok := payload.Fields[k]
		if !ok || field.GetStringValue() != v {
			return false
		}
	}
	return true
}

// RouteStreamStart forwards a StreamStart request to the plugin that provides
// the streaming tool. It uses SendStream on the plugin client to keep the QUIC
// stream open for multiple responses.
func (r *Router) RouteStreamStart(ctx context.Context, req *pluginv1.StreamStart) (<-chan *pluginv1.PluginResponse, error) {
	r.mu.RLock()
	rp := r.findToolPlugin(req.GetToolName(), "")
	r.mu.RUnlock()

	if rp == nil {
		return nil, fmt.Errorf("no plugin provides streaming tool %q", req.GetToolName())
	}

	return rp.Client.SendStream(ctx, &pluginv1.PluginRequest{
		RequestId: uuid.New().String(),
		Request: &pluginv1.PluginRequest_StreamStart{
			StreamStart: req,
		},
	})
}
