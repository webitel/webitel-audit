package server

import (
	"context"
	"io"
	"log/slog"
	"os"

	"go.uber.org/fx"

	"github.com/webitel/webitel-go-kit/pkg/logging"

	"github.com/webitel/webitel-audit/config"
)

func ProvideLogger(cfg *config.Config, lc fx.Lifecycle, otel *OTelLogs) (*slog.Logger, error) {
	logSettings := cfg.Log

	// The level is validated in config; an empty or unknown one falls back to info.
	var level slog.Level

	_ = level.UnmarshalText([]byte(logSettings.Level))

	opts := &slog.HandlerOptions{Level: level}

	newHandler := func(w io.Writer) slog.Handler {
		if logSettings.JSON {
			return slog.NewJSONHandler(w, opts)
		}

		return slog.NewTextHandler(w, opts)
	}

	var handlers []slog.Handler

	if logSettings.Console {
		handlers = append(handlers, newHandler(os.Stdout))
	}

	if logSettings.File != "" {
		f, err := os.OpenFile(logSettings.File, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}

		lc.Append(fx.Hook{
			OnStop: func(context.Context) error { return f.Close() },
		})

		handlers = append(handlers, newHandler(f))
	}

	if logSettings.Otel && otel.handler != nil {
		handlers = append(handlers, logging.NewLevelHandler(level, otel.handler))
	}

	if len(handlers) == 0 {
		handlers = append(handlers, newHandler(os.Stdout))
	}

	logger := slog.New(slog.NewMultiHandler(handlers...))
	slog.SetDefault(logger)

	return logger, nil
}
