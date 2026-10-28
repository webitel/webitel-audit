package postgres

import (
	"context"
	stderrors "errors"
	"net"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"

	"github.com/webitel/webitel-go-kit/infra/pgw"
	"github.com/webitel/webitel-go-kit/pkg/errors"
)

// SQLSTATE codes mapped by ParseError; pgw keeps the *pgconn.PgError reachable through errors.As.
const (
	codeNotNullViolation     = "23502"
	codeForeignKeyViolation  = "23503"
	codeUniqueViolation      = "23505"
	codeCheckViolation       = "23514"
	codeSerializationFailure = "40001"
	codeDeadlockDetected     = "40P01"
	classConnectionException = "08"
	classAdminShutdown       = "57P0" // 57P01 admin_shutdown, 57P02 crash_shutdown, 57P03 cannot_connect_now
)

// ParseError maps a database error to a coded go-kit error, keeping the original as the cause.
// An error that already carries a code is returned as is.
func ParseError(err error) error {
	if err == nil {
		return nil
	}

	if _, coded := errors.Lookup(err, errors.ErrKeyCode); coded {
		return err
	}

	if mapped := parsePgError(err); mapped != nil {
		return mapped
	}

	switch {
	case stderrors.Is(err, pgx.ErrNoRows):
		return errors.NotFound("entity not found", errors.WithID("store.pg.not_found"), errors.WithCause(err))
	case stderrors.Is(err, context.DeadlineExceeded):
		return errors.New("storage request timed out",
			errors.WithCode(codes.DeadlineExceeded), errors.WithID("store.pg.deadline"), errors.WithCause(err))
	case stderrors.Is(err, context.Canceled):
		return errors.New("storage request canceled",
			errors.WithCode(codes.Canceled), errors.WithID("store.pg.canceled"), errors.WithCause(err))
	case isUnavailable(err):
		return unavailable(err)
	}

	return errors.Internal("storage error", errors.WithID("store.pg.internal"), errors.WithCause(err))
}

// parsePgError maps the SQLSTATE of a server error; nil when err has none it knows.
func parsePgError(err error) error {
	var pgErr *pgconn.PgError
	if !stderrors.As(err, &pgErr) {
		return nil
	}

	switch pgErr.Code {
	case codeUniqueViolation:
		return errors.New("entity already exists",
			errors.WithCode(codes.AlreadyExists), errors.WithID("store.pg.unique"), errors.WithCause(err),
			errors.WithValue("constraint", pgErr.ConstraintName))
	case codeForeignKeyViolation:
		return errors.New("referenced entity does not exist or is still referenced",
			errors.WithCode(codes.FailedPrecondition), errors.WithID("store.pg.foreign_key"), errors.WithCause(err),
			errors.WithValue("constraint", pgErr.ConstraintName))
	case codeCheckViolation:
		return errors.InvalidArgument("value violates a constraint",
			errors.WithID("store.pg.check"), errors.WithCause(err),
			errors.WithValue("constraint", pgErr.ConstraintName))
	case codeNotNullViolation:
		return errors.InvalidArgument("required value is missing",
			errors.WithID("store.pg.not_null"), errors.WithCause(err),
			errors.WithValue("column", pgErr.ColumnName))
	case codeSerializationFailure, codeDeadlockDetected:
		return errors.Aborted("concurrent change, try again", errors.WithID("store.pg.conflict"), errors.WithCause(err))
	}

	if strings.HasPrefix(pgErr.Code, classConnectionException) || strings.HasPrefix(pgErr.Code, classAdminShutdown) {
		return unavailable(err)
	}

	return nil
}

func isUnavailable(err error) bool {
	var (
		netErr  net.Error
		dialErr *pgconn.ConnectError
	)

	return stderrors.Is(err, pgw.ErrUnreachable) ||
		stderrors.Is(err, pgconn.ErrConnClosed) ||
		stderrors.As(err, &netErr) ||
		stderrors.As(err, &dialErr)
}

func unavailable(err error) error {
	return errors.Unavailable("storage is unavailable", errors.WithID("store.pg.unavailable"), errors.WithCause(err))
}
