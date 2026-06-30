module github.com/deploymenttheory/weave

go 1.26.2

require (
	github.com/coder/websocket v1.8.15
	github.com/deploymenttheory/go-bindings-macosplatform v0.14.0
	github.com/deploymenttheory/go-sdk-vtpm2 v1.0.0
	github.com/deploymenttheory/go-sdk-winmediafoundry v0.6.1
	github.com/ebitengine/purego v0.10.1
	github.com/getkin/kin-openapi v0.140.0
	github.com/getsentry/sentry-go v0.47.0
	github.com/getsentry/sentry-go/otel v0.47.0
	github.com/go-chi/chi/v5 v5.3.0
	github.com/modelcontextprotocol/go-sdk v1.6.1
	go.opentelemetry.io/contrib/bridges/otelslog v0.19.0
	go.opentelemetry.io/otel v1.44.0
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.20.0
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.44.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.44.0
	go.opentelemetry.io/otel/exporters/stdout/stdoutlog v0.20.0
	go.opentelemetry.io/otel/exporters/stdout/stdoutmetric v1.44.0
	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.44.0
	go.opentelemetry.io/otel/log v0.20.0
	go.opentelemetry.io/otel/metric v1.44.0
	go.opentelemetry.io/otel/sdk v1.44.0
	go.opentelemetry.io/otel/sdk/log v0.20.0
	go.opentelemetry.io/otel/sdk/metric v1.44.0
	go.opentelemetry.io/otel/trace v1.44.0
	golang.org/x/crypto v0.53.0
	golang.org/x/sys v0.46.0
	google.golang.org/grpc v1.81.1
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
)

// go-sdk-vtpm2 is the Go-native, swtpm-compatible TPM 2.0 emulator weave's QEMU
// backend attaches as the Windows 11 vTPM (replaces the swtpm Homebrew binary).
// GPLv3 — linking it makes guestweave GPLv3.

require (
	github.com/anchore/go-lzo v0.1.0 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/diskfs/go-diskfs v1.9.3 // indirect
	github.com/djherbis/times v1.6.0 // indirect
	github.com/elliotwutingfeng/asciiset v0.0.0-20260129054604-cfde2086bc57 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-openapi/jsonpointer v0.22.5 // indirect
	github.com/go-openapi/swag/jsonname v0.25.5 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/mux v1.8.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.29.0 // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/oasdiff/yaml v0.1.0 // indirect
	github.com/oasdiff/yaml3 v0.0.13 // indirect
	github.com/pierrec/lz4/v4 v4.1.26 // indirect
	github.com/pkg/xattr v0.4.12 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/sirupsen/logrus v1.9.4 // indirect
	github.com/ulikunitz/xz v0.5.15 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.44.0 // indirect
	go.opentelemetry.io/proto/otlp v1.10.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	go.uber.org/zap v1.28.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260618152121-87f3d3e198d3 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260618152121-87f3d3e198d3 // indirect
	resty.dev/v3 v3.0.0-rc.2 // indirect
)
