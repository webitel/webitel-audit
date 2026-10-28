package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/webitel/webitel-go-kit/pkg/errors"
)

var testSortable = map[string]string{"name": "f.name", "created_at": "f.created_at"}

func TestParseSort(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"blank criteria", " , ,", nil},
		{"bare name ascends", "name", []string{"f.name ASC"}},
		{"plus ascends", "+name", []string{"f.name ASC"}},
		{"minus descends", "-created_at", []string{"f.created_at DESC"}},
		{"several", "name,-created_at", []string{"f.name ASC", "f.created_at DESC"}},
		{"spaces trimmed", " name , -created_at ", []string{"f.name ASC", "f.created_at DESC"}},
		{"case insensitive", "NAME", []string{"f.name ASC"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSort(tt.in, testSortable)

			require.NoError(t, err)

			if len(tt.want) == 0 {
				assert.Empty(t, got)

				return
			}

			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseSortRejects(t *testing.T) {
	for _, in := range []string{"unknown", "+", "-", "*name", "+-name"} {
		t.Run(in, func(t *testing.T) {
			got, err := ParseSort(in, testSortable)

			require.Error(t, err)
			assert.Nil(t, got)
			assert.Equal(t, codes.InvalidArgument, errors.Code(err))
		})
	}
}
