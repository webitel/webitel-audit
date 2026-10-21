package migrate

import (
	"log/slog"

	"github.com/urfave/cli/v2"

	"github.com/webitel/webitel-audit/config"
)

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
		Action: func(*cli.Context) error {
			if _, err := config.LoadMigrateConfig(); err != nil {
				return err
			}

			// Goose runner and the audit schema are added together with the database layer (DT-34).
			slog.Info("migrate: no migrations are available yet")

			return nil
		},
	}
}
