package migrate

import (
	"context"
	"hash/crc32"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/urfave/cli/v2"
	"go.uber.org/fx"

	"github.com/webitel/webitel-audit/cmd/server"
	"github.com/webitel/webitel-audit/config"
	"github.com/webitel/webitel-audit/internal/model"
	"github.com/webitel/webitel-audit/migrations"
)

// lockID is the advisory lock of audit migrations: goose's default id is shared by every service in the database.
var lockID = int64(crc32.ChecksumIEEE([]byte(model.ServiceName)))

func CMD() *cli.Command {
	return &cli.Command{
		Name:  "migrate",
		Usage: "Apply database migrations",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "config_file",
				Usage: "Path to the configuration file",
			},
		},
		Action: func(c *cli.Context) error {
			cfg, err := config.LoadMigrateConfig()
			if err != nil {
				return err
			}

			app := fx.New(
				fx.NopLogger,
				fx.Supply(cfg, &server.OTelLogs{}),
				fx.Provide(server.ProvideLogger),
				fx.Invoke(func(log *slog.Logger) error {
					return up(c.Context, cfg, log)
				}),
			)

			return app.Err()
		},
	}
}

// up applies every pending migration under the advisory lock.
func up(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	poolConfig, err := pgxpool.ParseConfig(cfg.Postgres.DSN)
	if err != nil {
		return err
	}

	db := stdlib.OpenDB(*poolConfig.ConnConfig)
	defer func() { _ = db.Close() }()

	locker, err := lock.NewPostgresSessionLocker(lock.WithLockID(lockID))
	if err != nil {
		return err
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.EmbedMigrations,
		goose.WithTableName(migrations.VersionTable),
		goose.WithSessionLocker(locker),
		goose.WithSlog(log),
		goose.WithVerbose(true),
	)
	if err != nil {
		return err
	}

	results, err := provider.Up(ctx)
	if err != nil {
		return err
	}

	log.Info("migrations are up to date", "applied", len(results))

	return nil
}
