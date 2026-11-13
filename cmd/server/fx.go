package server

import (
	"log/slog"
	"net/url"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	discoveryfx "github.com/webitel/webitel-go-kit/infra/discovery/fx"
	healthfx "github.com/webitel/webitel-go-kit/infra/health/fx"
	authfx "github.com/webitel/webitel-go-kit/infra/webitel/auth/fx"

	"github.com/webitel/webitel-audit/config"
	auditv1 "github.com/webitel/webitel-audit/gen/go/audit/v1"
	grpcserver "github.com/webitel/webitel-audit/infra/server/grpc"
	grpchandler "github.com/webitel/webitel-audit/internal/handler/grpc"
	"github.com/webitel/webitel-audit/internal/model"
	"github.com/webitel/webitel-audit/internal/service"
	"github.com/webitel/webitel-audit/internal/store/postgres"
)

func NewApp(cfg *config.Config) *fx.App {
	return fx.New(options(cfg))
}

func options(cfg *config.Config) fx.Option {
	return fx.Options(
		fx.WithLogger(func(l *slog.Logger) fxevent.Logger {
			fl := &fxevent.SlogLogger{Logger: l}
			fl.UseLogLevel(slog.LevelDebug) // routine wiring events stay out of info logs
			fl.UseErrorLevel(slog.LevelError)

			return fl
		}),

		fx.Provide(
			func() *config.Config { return cfg },
			ProvideOTel,
			ProvideLogger,
		),

		healthfx.Module(healthfx.Config{HTTPAddr: cfg.Health.Addr}),

		postgres.Module,
		service.Module,

		serving(cfg),
		grpchandler.Module,

		healthfx.Shutdown(),
	)
}

// serving wires the gRPC server, discovery and auth;
func serving(cfg *config.Config) fx.Option {
	return fx.Options(
		grpcserver.Module,
		discoveryfx.Module(discoveryfx.Config{
			Addr:      cfg.Consul.Addr,
			Name:      model.ServiceName,
			Version:   model.Version,
			Endpoints: []string{(&url.URL{Scheme: "grpc", Host: cfg.Service.Addr}).String()},
			Metadata:  map[string]string{"commit": model.Commit},
		}),
		authfx.Module(authfx.Config{}, auditv1.WebitelAPI),
	)
}
