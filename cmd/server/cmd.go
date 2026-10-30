package server

import (
	"context"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/webitel/webitel-audit/config"
)

// stopTimeout bounds the graceful shutdown: health drain (up to 20s), Deregister and gRPC GracefulStop (10s).
const stopTimeout = 45 * time.Second

func CMD() *cli.Command {
	return &cli.Command{
		Name:    "server",
		Aliases: []string{"s"},
		Usage:   "Run the audit server",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config_file",
				Usage: "Path to the configuration file",
			},
		},
		Action: func(c *cli.Context) error {
			cfg, err := config.LoadServerConfig()
			if err != nil {
				return err
			}

			app := NewApp(cfg)

			// Listen before Start so a signal during startup is not fatal. Start gets its own context:
			// fx rollback with a canceled one skips the stop hooks.
			ctx, stop := signal.NotifyContext(c.Context, syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			startCtx, cancelStart := context.WithTimeout(context.Background(), app.StartTimeout())
			defer cancelStart()

			if err := app.Start(startCtx); err != nil {
				return err
			}

			<-ctx.Done()

			stop()

			slog.Info("Shutting down...")

			stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
			defer cancel()

			return app.Stop(stopCtx)
		},
	}
}
