package server

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.38.0"
	"go.uber.org/fx"

	"github.com/webitel/webitel-go-kit/infra/discovery"
	otelsdk "github.com/webitel/webitel-go-kit/infra/otel/sdk"

	"github.com/webitel/webitel-audit/internal/model"

	// Register the OTel exporters selected by OTEL_*_EXPORTER.
	_ "github.com/webitel/webitel-go-kit/infra/otel/sdk/log/otlp"
	_ "github.com/webitel/webitel-go-kit/infra/otel/sdk/log/stdout"
	_ "github.com/webitel/webitel-go-kit/infra/otel/sdk/metric/otlp"
	_ "github.com/webitel/webitel-go-kit/infra/otel/sdk/metric/prometheus"
	_ "github.com/webitel/webitel-go-kit/infra/otel/sdk/metric/stdout"
	_ "github.com/webitel/webitel-go-kit/infra/otel/sdk/trace/otlp"
	_ "github.com/webitel/webitel-go-kit/infra/otel/sdk/trace/stdout"
)

type OTelLogs struct {
	handler slog.Handler
}

func ProvideOTel(lc fx.Lifecycle) (*OTelLogs, error) {
	service := resource.NewSchemaless(
		semconv.ServiceName(model.ServiceName),
		semconv.ServiceVersion(model.Version),
		semconv.ServiceInstanceID(discovery.GenerateInstanceID(model.ServiceName)),
		semconv.ServiceNamespace(model.ServiceNamespace),
	)

	logs := &OTelLogs{}

	shutdown, err := otelsdk.Configure(context.Background(),
		otelsdk.WithResource(service),
		otelsdk.WithLogBridge(func() { logs.handler = otelslog.NewHandler("slog") }),
	)
	if err != nil {
		return nil, err
	}

	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error { return shutdown(ctx) },
	})

	return logs, nil
}
