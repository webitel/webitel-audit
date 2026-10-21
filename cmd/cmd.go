package cmd

import (
	"os"

	"github.com/urfave/cli/v2"

	"github.com/webitel/webitel-audit/cmd/migrate"
	"github.com/webitel/webitel-audit/cmd/server"
	"github.com/webitel/webitel-audit/internal/model"
)

func Run() error {
	app := &cli.App{
		Name:    model.ServiceName,
		Usage:   "Audit service of the Webitel platform",
		Version: model.Version + " (" + model.Commit + ")",
		Commands: []*cli.Command{
			server.CMD(),
			migrate.CMD(),
		},
	}

	return app.Run(os.Args)
}
