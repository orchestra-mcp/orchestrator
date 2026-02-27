# Orchestra Orchestrator

Central hub that loads plugins, routes QUIC messages, and manages the plugin lifecycle.

## Install

```bash
go get github.com/orchestra-mcp/orchestrator
```

## Usage

```bash
# Build
go build -o bin/orchestrator ./cmd/

# Run with a plugin configuration file
bin/orchestrator --config plugins.yaml --workspace /path/to/project
```

## How It Works

The orchestrator implements a star topology -- all inter-plugin communication flows through it.

1. Reads `plugins.yaml` to discover plugin binaries
2. Starts each plugin binary as a subprocess
3. Waits for `READY <address>` on stderr from each plugin
4. Connects to each plugin over QUIC with mTLS
5. Builds routing tables: tool name to plugin, storage type to plugin
6. Forwards `PluginRequest`/`PluginResponse` messages between plugins

## Configuration

```yaml
plugins:
  - id: storage.markdown
    binary: bin/storage-markdown
    args: ["--workspace", "."]
  - id: tools.features
    binary: bin/tools-features
  - id: transport.stdio
    binary: bin/transport-stdio
```

## Architecture

```
         ┌────────────────────┐
         │    Orchestrator     │
         │  (QUIC hub + router)│
         └──────┬─────────────┘
                │ QUIC + mTLS
   ┌────────────┼────────────┐
   v            v            v
storage     tools       transport
markdown    features    stdio
```

## Related Packages

| Package | Description |
|---------|-------------|
| [sdk-go](https://github.com/orchestra-mcp/sdk-go) | Plugin SDK used by all Go plugins |
| [gen-go](https://github.com/orchestra-mcp/gen-go) | Generated Protobuf types |
| [proto](https://github.com/orchestra-mcp/proto) | Source `.proto` definitions |

## License

[MIT](LICENSE)
