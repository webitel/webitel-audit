package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/webitel/webitel-go-kit/pkg/errors"
)

var (
	testAllowed  = []string{"id", "name", "created_at"}
	testDefaults = []string{"id", "name"}
)

func TestParseFields(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil takes defaults", nil, []string{"id", "name"}},
		{"blanks take defaults", []string{"", " "}, []string{"id", "name"}},
		{"one per element", []string{"id", "created_at"}, []string{"id", "created_at"}},
		{"comma separated", []string{"id,created_at"}, []string{"id", "created_at"}},
		{"space separated", []string{"id name created_at"}, []string{"id", "name", "created_at"}},
		{"lowercased", []string{"ID", "Name"}, []string{"id", "name"}},
		{"repeats dropped keeping order", []string{"name", "id", "name"}, []string{"name", "id"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseFields(tt.in, testAllowed, testDefaults)

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseFieldsRejectsUnknown(t *testing.T) {
	for _, in := range [][]string{{"id", "secret"}, {"id,secret"}} {
		got, err := ParseFields(in, testAllowed, testDefaults)

		require.Error(t, err)
		assert.Nil(t, got)
		assert.Equal(t, codes.InvalidArgument, errors.Code(err))
	}
}

func TestParseFieldsReturnsDefaultsCopy(t *testing.T) {
	got, err := ParseFields(nil, testAllowed, testDefaults)
	require.NoError(t, err)

	got[0] = "changed"

	assert.Equal(t, "id", testDefaults[0])
}
