# webitel-audit

Audit service of the Webitel platform. Built on the shared go-kit
(`appconfig`, `infra/health`, `infra/otel`, `infra/pgw`) and wired with
[fx](https://github.com/uber-go/fx).

## Run

```sh
go run . server          # run the service
go run . migrate         # apply pending migrations to the audit schema
```

Configured only through environment variables or a config file
(`--config_file path`, or `CONFIG_FILE=path`, YAML or JSON). Copy
[`.env.example`](.env.example) to `.env` and export it before running
locally (`.env.otel.example` for OpenTelemetry). The configuration is
validated on start; an invalid one stops the service.

| Env | Default | Description |
|---|---|---|
| `SERVICE_ADDR` | `127.0.0.1:8080` | gRPC listen address (not used until the gRPC server arrives) |
| `HEALTH_ADDR` | not set | listen address of `/livez`, `/readyz`, `/healthz`; unset disables the probes |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_JSON` | `false` | JSON console output |
| `LOG_CONSOLE` | `true` | write logs to stdout |
| `LOG_FILE` | | write logs to a file |
| `LOG_OTEL` | `false` | add the OpenTelemetry log bridge |
| `POSTGRES_DSN` | not set | PostgreSQL connection string, required by `server` and `migrate` |
| `POSTGRES_MAX_OPEN_CONNS` | `0` | max connections of the server pool; `0` keeps the pgx default |

## Database

Tables live in the `audit` schema of the shared Webitel database. Migrations
are embedded in the binary and applied only by `migrate` (no `down`).

The server does not check the schema version on start.

## Tests

```sh
go test ./...
```

Mocks are generated with MockGen into `gen/mocks`.
