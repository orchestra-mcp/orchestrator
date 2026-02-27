package internal

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"time"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/google/uuid"
	"github.com/orchestra-mcp/sdk-go/plugin"
	"github.com/quic-go/quic-go"
)

// OrchestratorServer is the QUIC server that the orchestrator runs. Plugins
// connect to this server to make callback requests (e.g., a tools plugin
// needing to read from storage). The server dispatches incoming PluginRequest
// messages to the Router for tool calls and storage operations.
type OrchestratorServer struct {
	addr      string
	tlsConfig *tls.Config
	router    *Router
	listener  *quic.Listener
}

// NewOrchestratorServer creates a new server that will listen on the given
// address using the provided TLS configuration. All incoming requests are
// dispatched through the router.
func NewOrchestratorServer(addr string, tlsConfig *tls.Config, router *Router) *OrchestratorServer {
	return &OrchestratorServer{
		addr:      addr,
		tlsConfig: tlsConfig,
		router:    router,
	}
}

// Addr returns the actual listening address. Only valid after ListenAndServe
// has been called (it is set once the listener binds).
func (s *OrchestratorServer) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.addr
}

// ListenAndServe starts the QUIC listener and processes connections until the
// context is cancelled.
func (s *OrchestratorServer) ListenAndServe(ctx context.Context) error {
	listener, err := quic.ListenAddr(s.addr, s.tlsConfig, &quic.Config{
		MaxIdleTimeout:  5 * time.Minute,
		KeepAlivePeriod: 15 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("quic listen %s: %w", s.addr, err)
	}
	s.listener = listener
	defer listener.Close()

	log.Printf("orchestrator QUIC server listening on %s", listener.Addr().String())

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // graceful shutdown
			}
			return fmt.Errorf("accept connection: %w", err)
		}
		go s.handleConnection(ctx, conn)
	}
}

// handleConnection accepts streams on a single QUIC connection.
func (s *OrchestratorServer) handleConnection(ctx context.Context, conn quic.Connection) {
	defer conn.CloseWithError(0, "")
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			return
		}
		go s.handleStream(ctx, stream)
	}
}

// handleStream reads a PluginRequest, dispatches it, writes the response, and
// closes the stream.
func (s *OrchestratorServer) handleStream(ctx context.Context, stream quic.Stream) {
	defer stream.Close()

	var req pluginv1.PluginRequest
	if err := plugin.ReadMessage(stream, &req); err != nil {
		log.Printf("orchestrator server: read request: %v", err)
		return
	}

	resp := s.dispatch(ctx, &req)
	resp.RequestId = req.GetRequestId()

	if err := plugin.WriteMessage(stream, resp); err != nil {
		log.Printf("orchestrator server: write response: %v", err)
	}
}

// dispatch routes a PluginRequest to the appropriate handler based on the
// request type. The orchestrator handles:
// - tool_call     -> Router.RouteToolCall
// - list_tools    -> Router.ListAllTools
// - storage_read  -> Router.RouteStorageRead
// - storage_write -> Router.RouteStorageWrite
// - storage_delete -> Router.RouteStorageDelete
// - storage_list  -> Router.RouteStorageList
// - health        -> immediate healthy response
// - register      -> immediate accepted response (for plugins connecting back)
func (s *OrchestratorServer) dispatch(ctx context.Context, req *pluginv1.PluginRequest) *pluginv1.PluginResponse {
	switch r := req.Request.(type) {

	case *pluginv1.PluginRequest_Register:
		// A plugin is connecting to the orchestrator and registering itself.
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_Register{
				Register: &pluginv1.RegistrationResult{
					Accepted: true,
				},
			},
		}

	case *pluginv1.PluginRequest_Health:
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_Health{
				Health: &pluginv1.HealthResult{
					Status:  pluginv1.HealthResult_STATUS_HEALTHY,
					Message: "orchestrator ok",
				},
			},
		}

	case *pluginv1.PluginRequest_ListTools:
		tools, err := s.router.ListAllTools(ctx)
		if err != nil {
			return errorResponse("list_tools_error", err.Error())
		}
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_ListTools{
				ListTools: &pluginv1.ListToolsResponse{
					Tools: tools,
				},
			},
		}

	case *pluginv1.PluginRequest_ToolCall:
		result, err := s.router.RouteToolCall(ctx, r.ToolCall)
		if err != nil {
			return errorResponse("tool_routing_error", err.Error())
		}
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_ToolCall{
				ToolCall: result,
			},
		}

	case *pluginv1.PluginRequest_ListPrompts:
		prompts, err := s.router.ListAllPrompts(ctx)
		if err != nil {
			return errorResponse("list_prompts_error", err.Error())
		}
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_ListPrompts{
				ListPrompts: &pluginv1.ListPromptsResponse{
					Prompts: prompts,
				},
			},
		}

	case *pluginv1.PluginRequest_PromptGet:
		result, err := s.router.RoutePromptGet(ctx, r.PromptGet)
		if err != nil {
			return errorResponse("prompt_routing_error", err.Error())
		}
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_PromptGet{
				PromptGet: result,
			},
		}

	case *pluginv1.PluginRequest_StorageRead:
		result, err := s.router.RouteStorageRead(ctx, r.StorageRead)
		if err != nil {
			return storageReadErrorResponse(err)
		}
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_StorageRead{
				StorageRead: result,
			},
		}

	case *pluginv1.PluginRequest_StorageWrite:
		result, err := s.router.RouteStorageWrite(ctx, r.StorageWrite)
		if err != nil {
			return storageWriteErrorResponse(err)
		}
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_StorageWrite{
				StorageWrite: result,
			},
		}

	case *pluginv1.PluginRequest_StorageDelete:
		result, err := s.router.RouteStorageDelete(ctx, r.StorageDelete)
		if err != nil {
			return storageDeleteErrorResponse(err)
		}
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_StorageDelete{
				StorageDelete: result,
			},
		}

	case *pluginv1.PluginRequest_StorageList:
		result, err := s.router.RouteStorageList(ctx, r.StorageList)
		if err != nil {
			return storageListErrorResponse(err)
		}
		return &pluginv1.PluginResponse{
			Response: &pluginv1.PluginResponse_StorageList{
				StorageList: result,
			},
		}

	default:
		return errorResponse("unknown_request", "unrecognized request type")
	}
}

// errorResponse builds a generic error response using the tool call response
// envelope (since there is no dedicated error type in the protocol).
func errorResponse(code, message string) *pluginv1.PluginResponse {
	return &pluginv1.PluginResponse{
		Response: &pluginv1.PluginResponse_ToolCall{
			ToolCall: &pluginv1.ToolResponse{
				Success:      false,
				ErrorCode:    code,
				ErrorMessage: message,
			},
		},
	}
}

// storageReadErrorResponse wraps a storage-read error into a response with
// empty content. There is no explicit error field on StorageReadResponse, so
// we return an empty response with the error logged.
func storageReadErrorResponse(err error) *pluginv1.PluginResponse {
	log.Printf("storage_read error: %v", err)
	return &pluginv1.PluginResponse{
		Response: &pluginv1.PluginResponse_StorageRead{
			StorageRead: &pluginv1.StorageReadResponse{},
		},
	}
}

// storageWriteErrorResponse wraps a storage-write error.
func storageWriteErrorResponse(err error) *pluginv1.PluginResponse {
	return &pluginv1.PluginResponse{
		Response: &pluginv1.PluginResponse_StorageWrite{
			StorageWrite: &pluginv1.StorageWriteResponse{
				Success: false,
				Error:   err.Error(),
			},
		},
	}
}

// storageDeleteErrorResponse wraps a storage-delete error.
func storageDeleteErrorResponse(err error) *pluginv1.PluginResponse {
	return &pluginv1.PluginResponse{
		Response: &pluginv1.PluginResponse_StorageDelete{
			StorageDelete: &pluginv1.StorageDeleteResponse{
				Success: false,
			},
		},
	}
}

// storageListErrorResponse wraps a storage-list error.
func storageListErrorResponse(err error) *pluginv1.PluginResponse {
	log.Printf("storage_list error: %v", err)
	return &pluginv1.PluginResponse{
		Response: &pluginv1.PluginResponse_StorageList{
			StorageList: &pluginv1.StorageListResponse{},
		},
	}
}

// newRequestID generates a UUID for request correlation.
func newRequestID() string {
	return uuid.New().String()
}
