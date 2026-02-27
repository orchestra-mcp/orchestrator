# Architecture

## Overview

The orchestrator is the central hub of the Orchestra plugin system. It loads plugin binaries, manages their lifecycle, and routes all inter-plugin communication. Plugins never communicate directly with each other -- all messages flow through the orchestrator (star topology).

## Topology

```
                    +---------------------+
                    |    Orchestrator      |
                    |  (QUIC hub + router) |
                    +----------+----------+
                               |
              QUIC + mTLS      |      QUIC + mTLS
         +---------------------+---------------------+
         |                     |                     |
   +-----+------+    +--------+--------+    +-------+-------+
   |  storage   |    |     tools       |    |   transport   |
   |  markdown  |    |    features     |    |     stdio     |
   +------------+    +-----------------+    +---------------+
```

## Components

### Config (`internal/config.go`)

Parses `plugins.yaml` into a `Config` struct containing:
- `listen_addr` -- QUIC server bind address (default: `localhost:50100`)
- `certs_dir` -- mTLS certificate directory (default: `~/.orchestra/certs`)
- `plugins` -- List of `PluginConfig` entries

### Loader (`internal/loader.go`)

Manages plugin subprocess lifecycle:

1. **Start** -- Exec the plugin binary with `--orchestrator-addr`, `--listen-addr=localhost:0`, `--certs-dir`
2. **Wait for READY** -- Scan stderr for `READY <addr>` (10-second timeout)
3. **QUIC Connect** -- Establish mTLS QUIC connection to the plugin
4. **Register** -- Send `PluginManifest` registration
5. **Boot** -- Send `BootRequest` with config from `plugins.yaml`
6. **ListTools** -- Query the plugin for its tool definitions to populate routing tables
7. **Stop** -- Send `ShutdownRequest`, close QUIC connection, kill process

The `RunningPlugin` struct holds the subprocess handle, QUIC client, manifest, and address.

### Router (`internal/router.go`)

Maintains two routing tables:
- `toolRoutes`: `toolName -> *RunningPlugin`
- `storageRoutes`: `storageType -> *RunningPlugin`

When a plugin registers, the router indexes its `provides_tools` and `provides_storage` declarations. Routing methods:

| Method | Lookup Key | Description |
|---|---|---|
| `RouteToolCall` | `tool_name` | Forward tool invocation to the owning plugin |
| `RouteStorageRead` | `storage_type` | Forward read to the storage plugin (default: `"markdown"`) |
| `RouteStorageWrite` | `storage_type` | Forward write |
| `RouteStorageDelete` | `storage_type` | Forward delete |
| `RouteStorageList` | `storage_type` | Forward list |
| `ListAllTools` | (all plugins) | Aggregate tool definitions from every registered plugin |

### Server (`internal/server.go`)

The `OrchestratorServer` is a QUIC server that plugins connect to for making callback requests. For example, the `tools.features` plugin connects to this server to read/write storage through the `storage.markdown` plugin.

The server's `dispatch` method routes incoming `PluginRequest` messages:
- `tool_call` -> `Router.RouteToolCall`
- `list_tools` -> `Router.ListAllTools`
- `storage_*` -> `Router.RouteStorage*`
- `health` -> immediate response
- `register` -> immediate accept

### Orchestrator (`internal/orchestrator.go`)

The top-level struct that wires everything together:

1. Generate mTLS server certificate
2. Start QUIC server
3. Iterate `plugins.yaml` entries, skip disabled plugins
4. Call `Loader.StartPlugin` for each enabled plugin
5. Call `Router.RegisterPlugin` with the running plugin
6. Block until context cancelled
7. Call `Loader.StopAll` on shutdown

## Request Flow Example

A tool call from MCP (via transport-stdio) through to storage:

```
stdin JSON-RPC                                     Orchestrator
  |                                                     |
  | tools/call "create_feature"                         |
  +----> transport-stdio                                |
            |                                           |
            | PluginRequest(tool_call)                  |
            +----> Orchestrator QUIC Server              |
                      |                                 |
                      | RouteToolCall("create_feature") |
                      +----> tools-features plugin      |
                                |                       |
                                | PluginRequest(storage_write)
                                +----> Orchestrator QUIC Server
                                          |
                                          | RouteStorageWrite("markdown")
                                          +----> storage-markdown plugin
                                                    |
                                                    | Write file to disk
                                                    | Return response
                                          <---------+
                                |  <-----------------+
                      <---------+
            <---------+
  <---------+
stdout JSON-RPC response
```
