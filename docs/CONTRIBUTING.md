# Contributing to orchestrator

## Prerequisites

- Go 1.23+
- `gofmt`, `go vet`
- The `sdk-go` and `gen-go` modules

## Development Setup

```bash
git clone https://github.com/orchestra-mcp/orchestrator.git
cd orchestrator
go mod download
go build ./cmd/...
```

## Running Locally

Build the orchestrator and plugin binaries, then start with a config file:

```bash
go build -o bin/orchestrator ./cmd/
./bin/orchestrator --config=plugins.yaml
```

The orchestrator logs to stderr. Plugin output is prefixed with the plugin ID.

## Running Tests

```bash
go test ./...
```

## Code Style

- Run `gofmt` on all files.
- Run `go vet ./...` before committing.
- All exported functions and types must have doc comments.
- Error handling: wrap errors with context via `fmt.Errorf("...: %w", err)`.
- Use `context.Context` through the entire call chain.
- Use `sync.RWMutex` for concurrent map access in the router.

## Architecture Notes

- The orchestrator is intentionally small (~500 lines of core logic). Resist adding features to it.
- All business logic belongs in plugins, not the orchestrator.
- The router only does lookup + forward. No transformation of requests.
- Plugin loading is sequential at startup. Parallel loading may be added later.

## Pull Request Process

1. Fork the repository and create a feature branch from `main`.
2. Write or update tests for your changes.
3. Run `go test ./...` and `go vet ./...`.
4. Keep PRs focused. One feature or fix per PR.
5. Update `docs/CONFIGURATION.md` if adding new config fields.

## Related Repositories

- [orchestra-mcp/proto](https://github.com/orchestra-mcp/proto) -- Protobuf schema
- [orchestra-mcp/gen-go](https://github.com/orchestra-mcp/gen-go) -- Generated Go types
- [orchestra-mcp/sdk-go](https://github.com/orchestra-mcp/sdk-go) -- Go Plugin SDK
- [orchestra-mcp](https://github.com/orchestra-mcp) -- Organization home
