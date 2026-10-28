package migrations

import (
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrationsHaveUpAndDown(t *testing.T) {
	names, err := fs.Glob(EmbedMigrations, "*.sql")
	require.NoError(t, err)
	require.NotEmpty(t, names)

	for _, name := range names {
		body, err := fs.ReadFile(EmbedMigrations, name)
		require.NoError(t, err)

		assert.Contains(t, string(body), "-- +goose Up", name)
		assert.Contains(t, string(body), "-- +goose Down", name)
	}
}
