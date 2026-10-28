package postgres

import (
	"context"

	"go.uber.org/fx"

	"github.com/webitel/webitel-go-kit/infra/health"
	"github.com/webitel/webitel-go-kit/infra/pgw"

	"github.com/webitel/webitel-audit/config"
	"github.com/webitel/webitel-audit/internal/model"
)

var Module = fx.Module("postgres",
	fx.Provide(ProvidePoolManager),
	fx.Invoke(registerHealth),
)

func ProvidePoolManager(cfg *config.Config, lc fx.Lifecycle) (*pgw.PoolManager, error) {
	pm, err := pgw.NewPoolManager(context.Background(),
		pgw.WithApplicationName(model.ServiceName),
		pgw.WithPrimaryConfig(pgw.PrimaryConfig{DSN: cfg.Postgres.DSN, MaxConns: cfg.Postgres.MaxOpenConns}),
	)
	if err != nil {
		return nil, err
	}

	lc.Append(fx.Hook{
		OnStop: func(context.Context) error {
			pm.Close()

			return nil
		},
	})

	return pm, nil
}

func registerHealth(h *health.Registry, pm *pgw.PoolManager) {
	h.Informational("postgres", func(ctx context.Context) error {
		primary, err := pm.Primary()
		if err != nil {
			return err
		}

		_, err = primary.Exec(ctx, "SELECT 1")

		return err
	})
}
