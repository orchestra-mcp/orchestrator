# Configuration

The orchestrator reads its configuration from a YAML file (default: `plugins.yaml`).

## File Format

```yaml
# Address for the orchestrator's QUIC server.
# Plugins connect here for callback requests (storage, cross-plugin tool calls).
# Default: "localhost:50100"
listen_addr: "localhost:50100"

# Directory for mTLS certificates (CA, server, and client certs).
# Supports ~ for home directory expansion.
# Default: "~/.orchestra/certs"
certs_dir: "~/.orchestra/certs"

# List of plugins to load.
plugins:
  - id: storage.markdown
    binary: ./bin/storage-markdown
    enabled: true
    provides_storage:
      - markdown
    args:
      - --workspace=.
    config:
      workspace: "."

  - id: tools.features
    binary: ./bin/tools-features
    enabled: true

  - id: some.custom.plugin
    binary: /usr/local/bin/my-plugin
    enabled: false
    env:
      MY_ENV_VAR: "value"
    config:
      api_key: "abc123"
```

## Plugin Config Fields

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Unique plugin identifier (e.g., `storage.markdown`, `tools.features`) |
| `binary` | string | yes | Path to the plugin binary (absolute or relative to CWD) |
| `enabled` | bool | yes | Whether to load this plugin at startup |
| `args` | []string | no | Additional command-line arguments passed to the plugin binary |
| `env` | map[string]string | no | Environment variables set for the plugin process |
| `config` | map[string]string | no | Key-value pairs sent to the plugin during Boot |
| `provides_storage` | []string | no | Storage types this plugin provides (used for routing) |

## Plugin Process Arguments

The orchestrator automatically passes these flags to every plugin binary:

| Flag | Value |
|---|---|
| `--orchestrator-addr` | The orchestrator's actual QUIC listen address |
| `--listen-addr` | `localhost:0` (OS assigns a random port) |
| `--certs-dir` | The configured `certs_dir` path |

These are appended after any `args` from the config.

## Defaults

If a field is omitted:
- `listen_addr` defaults to `localhost:50100`
- `certs_dir` defaults to `~/.orchestra/certs`

## CLI Overrides

The orchestrator binary accepts flags that override the config file:

```bash
orchestrator \
  --config=plugins.yaml \
  --listen-addr=localhost:9200 \
  --certs-dir=/tmp/certs
```

| Flag | Description |
|---|---|
| `--config` | Path to the YAML config file (default: `plugins.yaml`) |
| `--listen-addr` | Override `listen_addr` from config |
| `--certs-dir` | Override `certs_dir` from config |
