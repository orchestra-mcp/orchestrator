module github.com/orchestra-mcp/orchestrator

go 1.23

require (
	github.com/google/uuid v1.6.0
	github.com/orchestra-mcp/gen-go v0.0.0
	github.com/orchestra-mcp/sdk-go v0.0.0
	github.com/quic-go/quic-go v0.50.0
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/go-task/slim-sprig v0.0.0-20230315185526-52ccab3ef572 // indirect
	github.com/google/pprof v0.0.0-20210407192527-94a9f03dee38 // indirect
	github.com/onsi/ginkgo/v2 v2.9.5 // indirect
	go.uber.org/mock v0.5.0 // indirect
	golang.org/x/crypto v0.26.0 // indirect
	golang.org/x/exp v0.0.0-20240506185415-9bf2ced13842 // indirect
	golang.org/x/mod v0.18.0 // indirect
	golang.org/x/net v0.28.0 // indirect
	golang.org/x/sync v0.8.0 // indirect
	golang.org/x/sys v0.23.0 // indirect
	golang.org/x/tools v0.22.0 // indirect
)

replace (
	github.com/orchestra-mcp/gen-go => ../gen-go
	github.com/orchestra-mcp/sdk-go => ../sdk-go
)
