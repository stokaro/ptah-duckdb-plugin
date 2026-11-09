module github.com/stokaro/ptah-duckdb-plugin

go 1.26.5

toolchain go1.27.3

require (
	github.com/duckdb/duckdb-go/v2 v2.20000.0
	github.com/frankban/quicktest v1.14.6
	golang.org/x/mod v0.41.0
	golang.org/x/sys v0.49.0
	google.golang.org/grpc v1.86.0-dev
	google.golang.org/protobuf v1.36.12
	gopkg.in/yaml.v3 v3.0.1
	ptah.run/provider-sdk v0.40.0
	ptah.run/sqlsyntax v0.2.0
)

require (
	github.com/apache/arrow-go/v18 v18.5.1 // indirect
	github.com/duckdb/duckdb-go-bindings v0.20000.0 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/go-extras/nolintguard v0.4.2 // indirect
	github.com/go-extras/qtlint v1.15.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/flatbuffers v25.12.19+incompatible // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/go-plugin v1.8.0 // indirect
	github.com/hashicorp/yamux v0.1.2 // indirect
	github.com/klauspost/compress v1.18.3 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-colorable v0.1.16 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/oklog/run v1.2.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.25 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	github.com/stokaro/teststyle v0.2.0 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.duckdb.org/libduckdb/darwin-amd64 v0.20000.0 // indirect
	go.duckdb.org/libduckdb/darwin-arm64 v0.20000.0 // indirect
	go.duckdb.org/libduckdb/linux-amd64 v0.20000.0 // indirect
	go.duckdb.org/libduckdb/linux-arm64 v0.20000.0 // indirect
	go.duckdb.org/libduckdb/windows-amd64 v0.20000.0 // indirect
	golang.org/x/exp v0.0.0-20260112195511-716be5621a96 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/telemetry v0.0.0-20260924152758-ed294f943157 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/tools v0.51.0 // indirect
	golang.org/x/xerrors v0.0.0-20240903120638-7835f813f4da // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261104224025-d1e00e9f33ab // indirect
)

tool (
	github.com/go-extras/nolintguard/cmd/nolintguard
	github.com/go-extras/qtlint/cmd/qtlint
	github.com/stokaro/teststyle/cmd/teststyle
)
