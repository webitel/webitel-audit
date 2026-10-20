package server

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/webitel/webitel-audit/config"
)

func TestGraphIsValid(t *testing.T) {
	cfg := &config.Config{Service: config.ServiceConfig{Addr: "127.0.0.1:0"}}

	require.NoError(t, fx.ValidateApp(options(cfg)))
}
