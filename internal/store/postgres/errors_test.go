package postgres

import (
	"context"
	stderrors "errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	"github.com/webitel/webitel-go-kit/infra/pgw"
	"github.com/webitel/webitel-go-kit/pkg/errors"
)

// realConnectError is a genuine *pgconn.ConnectError produced by a doomed dial (port 1 refuses
// immediately); unlike a hand-built one it has no nil fields, so logging or formatting it is safe.
func realConnectError(t *testing.T) error {
	t.Helper()

	_, err := pgconn.Connect(context.Background(), "postgres://u:p@127.0.0.1:1/db?connect_timeout=1")
	require.Error(t, err)

	return err
}

func TestParseError(t *testing.T) {
	tests := []struct {
		name string
		in   error
		want codes.Code
	}{
		{"no rows", pgx.ErrNoRows, codes.NotFound},
		{"wrapped no rows", fmt.Errorf("get: %w", pgx.ErrNoRows), codes.NotFound},
		{"deadline", context.DeadlineExceeded, codes.DeadlineExceeded},
		{"canceled", context.Canceled, codes.Canceled},
		{"primary unreachable", pgw.ErrUnreachable, codes.Unavailable},
		{"connection closed", pgconn.ErrConnClosed, codes.Unavailable},
		{"network error mid-query", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, codes.Unavailable},
		{"raw connection exception", &pgconn.PgError{Code: "08006"}, codes.Unavailable},
		{"admin shutdown", &pgconn.PgError{Code: "57P01"}, codes.Unavailable},
		{"unique", &pgconn.PgError{Code: "23505"}, codes.AlreadyExists},
		{"foreign key", &pgconn.PgError{Code: "23503"}, codes.FailedPrecondition},
		{"check", &pgconn.PgError{Code: "23514"}, codes.InvalidArgument},
		{"not null", &pgconn.PgError{Code: "23502"}, codes.InvalidArgument},
		{"serialization failure", &pgconn.PgError{Code: "40001"}, codes.Aborted},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, codes.Aborted},
		{"unknown sqlstate", &pgconn.PgError{Code: "42P01"}, codes.Internal},
		{"plain error", stderrors.New("boom"), codes.Internal},
		{"go-kit error without code", errors.New("boom"), codes.Internal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseError(tt.in)

			require.Error(t, got)
			assert.Equal(t, tt.want, errors.Code(got))
			assert.ErrorIs(t, got, tt.in)
		})
	}
}

func TestParseErrorConnectFailure(t *testing.T) {
	err := realConnectError(t)

	got := ParseError(err)

	require.Error(t, got)
	assert.Equal(t, codes.Unavailable, errors.Code(got))
	assert.ErrorIs(t, got, err)
}

func TestParseErrorNil(t *testing.T) {
	require.NoError(t, ParseError(nil))
}

func TestParseErrorKeepsCodedError(t *testing.T) {
	in := errors.NotFound("article not found")

	assert.Equal(t, in, ParseError(in))
}

func TestParseErrorConstraintValue(t *testing.T) {
	got := ParseError(&pgconn.PgError{Code: "23505", ConstraintName: "form_name_key"})

	assert.Equal(t, "form_name_key", errors.Value(got, "constraint"))
}
