package server

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	healthfx "github.com/webitel/webitel-go-kit/infra/health/fx"

	"github.com/webitel/webitel-audit/config"
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

		healthfx.Shutdown(),
	)
}
