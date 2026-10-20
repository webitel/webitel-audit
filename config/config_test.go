package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigValidate(t *testing.T) {
	t.Run("defaults log level", func(t *testing.T) {
		cfg := &Config{Service: ServiceConfig{Addr: "127.0.0.1:8080"}}

		require.NoError(t, cfg.validate())
		assert.Equal(t, "info", cfg.Log.Level)
	})

	t.Run("keeps explicit log level", func(t *testing.T) {
		cfg := &Config{Service: ServiceConfig{Addr: "127.0.0.1:8080"}}
		cfg.Log.Level = "debug"

		require.NoError(t, cfg.validate())
		assert.Equal(t, "debug", cfg.Log.Level)
	})

	t.Run("requires service addr", func(t *testing.T) {
		err := (&Config{}).validate()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "service.addr")
	})

	t.Run("accepts level in any case", func(t *testing.T) {
		cfg := &Config{Service: ServiceConfig{Addr: "127.0.0.1:8080"}}
		cfg.Log.Level = "DEBUG"

		require.NoError(t, cfg.validate())
	})

	t.Run("rejects unknown level", func(t *testing.T) {
		cfg := &Config{Service: ServiceConfig{Addr: "127.0.0.1:8080"}}
		cfg.Log.Level = "warning"

		err := cfg.validate()

		require.Error(t, err)
		assert.Contains(t, err.Error(), "log.level")
	})
}
