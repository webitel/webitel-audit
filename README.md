# webitel-audit

Audit service of the Webitel platform. It is built on the shared go-kit
(`appconfig`, `infra/health`, `infra/otel`) and wired with
[fx](https://github.com/uber-go/fx).

This is the service platform only: business API, database layer and gRPC
server are added by the following tasks of the DT-30 epic.

## Run

```sh
go run . server          # run the service
go run . migrate         # apply migrations; for now it only loads the config and exits (no migrations yet)
go run . --version
```

The service is configured only through environment variables or a config file
(`--config_file path` with two dashes, or `CONFIG_FILE=path`, YAML or JSON). Command-line flags
such as `--log.level` are not supported. See [`.env.example`](.env.example) for
the variables and [`.env.otel.example`](.env.otel.example) for the OpenTelemetry
ones. Copy `.env.example` to `.env` and export it before running locally.

## Configuration

| Env | Default | Description |
|---|---|---|
| `SERVICE_ADDR` | `127.0.0.1:8080` | gRPC listen address (required; not used until the gRPC server arrives) |
| `HEALTH_ADDR` | not set | listen address of `/livez`, `/readyz`, `/healthz`; when it is not set the probes are off |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` (any case); anything else stops the service on start. Also filters records sent to OpenTelemetry |
| `LOG_JSON` | `false` | JSON console output |
| `LOG_CONSOLE` | `true` | write logs to stdout |
| `LOG_FILE` | | write logs to a file |
| `LOG_OTEL` | `false` | add the OpenTelemetry log bridge; it ships logs only when `OTEL_LOGS_EXPORTER` is set. Traces and metrics follow `OTEL_TRACES_EXPORTER` and `OTEL_METRICS_EXPORTER` regardless |

The configuration is validated on start; an invalid one stops the service.

## Health

`/livez` answers 200 while the process runs. `/readyz` and `/healthz` answer
200 only when every critical check passes. The registry has no checks yet, so
both answer 503 until the database check arrives. On `SIGTERM` the service is
marked not-ready, waits a 15s drain period and then stops (30s limit). The probe
port closes at once, so during the drain a probe gets "connection refused"
rather than 503. A second signal stops the process immediately.

## Build

The version is set at link time:

```sh
go build -ldflags "-X github.com/webitel/webitel-audit/internal/model.Version=1.0.0 \
  -X github.com/webitel/webitel-audit/internal/model.Commit=$(git rev-parse --short HEAD)" .
```

`--version` prints both values. The CI build does not pass them yet: the build
config lives in `webitel/reusable-configs`.

## Tests

```sh
go test ./...
```

Tests use testify. Mocks are generated with MockGen into `gen/mocks`, see the
package comment there.
