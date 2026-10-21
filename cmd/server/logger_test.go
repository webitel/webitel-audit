package server

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx/fxtest"

	"github.com/webitel/webitel-audit/config"
)

func TestProvideLoggerWritesFileAtConfiguredLevel(t *testing.T) {
	prev := slog.Default()

	t.Cleanup(func() { slog.SetDefault(prev) })

	path := filepath.Join(t.TempDir(), "audit.log")
	cfg := &config.Config{}
	cfg.Log.Level = "warn"
	cfg.Log.File = path

	lc := fxtest.NewLifecycle(t)
	logger, err := ProvideLogger(cfg, lc, &OTelLogs{})
	require.NoError(t, err)

	logger.Info("hidden-info")
	logger.Warn("shown-warn")
	lc.RequireStart().RequireStop()

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(got), "shown-warn")
	assert.NotContains(t, string(got), "hidden-info")
}

// captureHandler records the messages it receives.
type captureHandler struct{ msgs *[]string }

func (captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h captureHandler) Handle(_ context.Context, r slog.Record) error {
	*h.msgs = append(*h.msgs, r.Message)

	return nil
}

func (h captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h captureHandler) WithGroup(string) slog.Handler      { return h }

func TestProvideLoggerFiltersOTelBridgeByLevel(t *testing.T) {
	prev := slog.Default()

	t.Cleanup(func() { slog.SetDefault(prev) })

	var got []string

	cfg := &config.Config{}
	cfg.Log.Level = "warn"
	cfg.Log.Otel = true

	logger, err := ProvideLogger(cfg, fxtest.NewLifecycle(t), &OTelLogs{handler: captureHandler{msgs: &got}})
	require.NoError(t, err)

	logger.Info("hidden-info")
	logger.Warn("shown-warn")

	assert.Equal(t, []string{"shown-warn"}, got)
}
