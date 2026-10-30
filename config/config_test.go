package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/webitel/webitel-go-kit/appconfig"
)

func validConfig() *Config {
	return &Config{
		Service:  ServiceConfig{Addr: "127.0.0.1:8080"},
		Consul:   appconfig.Consul{Addr: "127.0.0.1:8500"},
		Postgres: appconfig.Postgres{DSN: "postgres://127.0.0.1:5432/webitel"},
	}
}

func TestConfigValidate(t *testing.T) {
	t.Run("defaults log level", func(t *testing.T) {
		cfg := validConfig()

		require.NoError(t, cfg.validate())
		assert.Equal(t, "info", cfg.Log.Level)
	})

	t.Run("keeps explicit log level", func(t *testing.T) {
		cfg := validConfig()
		cfg.Log.Level = "debug"

		require.NoError(t, cfg.validate())
		assert.Equal(t, "debug", cfg.Log.Level)
	})

	t.Run("requires service addr", func(t *testing.T) {
		err := (&Config{}).validate()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "service.addr")
	})

	t.Run("rejects wildcard service addr", func(t *testing.T) {
		for _, addr := range []string{":8080", "0.0.0.0:8080", "[::]:8080", "no-port"} {
			cfg := validConfig()
			cfg.Service.Addr = addr

			assert.Error(t, cfg.validate(), addr)
		}
	})

	t.Run("rejects service addr without a usable port", func(t *testing.T) {
		for _, addr := range []string{"10.0.0.5:", "10.0.0.5:0", "10.0.0.5:http", "10.0.0.5:70000"} {
			cfg := validConfig()
			cfg.Service.Addr = addr

			assert.Error(t, cfg.validate(), addr)
		}
	})

	t.Run("requires consul addr", func(t *testing.T) {
		cfg := validConfig()
		cfg.Consul.Addr = ""

		err := cfg.validate()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "consul.addr")
	})

	t.Run("verified certs need cert paths", func(t *testing.T) {
		cfg := validConfig()
		cfg.Service.Connection.VerifyCerts = true

		require.Error(t, cfg.validate())

		cfg.Service.Connection.CA, cfg.Service.Connection.Cert, cfg.Service.Connection.Key = "ca", "cert", "key"
		require.NoError(t, cfg.validate())
	})

	t.Run("requires postgres dsn", func(t *testing.T) {
		cfg := validConfig()
		cfg.Postgres.DSN = ""

		err := cfg.validate()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "postgres.dsn")
	})

	t.Run("accepts level in any case", func(t *testing.T) {
		cfg := validConfig()
		cfg.Log.Level = "DEBUG"

		require.NoError(t, cfg.validate())
	})

	t.Run("rejects unknown level", func(t *testing.T) {
		cfg := validConfig()
		cfg.Log.Level = "warning"

		err := cfg.validate()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "log.level")
	})
}

func TestConfigValidateMigrate(t *testing.T) {
	t.Run("does not need service addr", func(t *testing.T) {
		cfg := &Config{Postgres: appconfig.Postgres{DSN: "postgres://127.0.0.1:5432/webitel"}}

		require.NoError(t, cfg.validateMigrate())
		assert.Equal(t, "info", cfg.Log.Level)
	})

	t.Run("requires postgres dsn", func(t *testing.T) {
		err := (&Config{}).validateMigrate()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "postgres.dsn")
	})

	t.Run("rejects unknown level", func(t *testing.T) {
		cfg := &Config{Postgres: appconfig.Postgres{DSN: "postgres://127.0.0.1:5432/webitel"}}
		cfg.Log.Level = "warning"

		err := cfg.validateMigrate()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "log.level")
	})
}
