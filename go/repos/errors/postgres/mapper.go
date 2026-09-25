// Package postgres provides PostgreSQL-specific DB error mapping.
// It translates pgx and pgconn errors into core/errors.AppError with
// domain-appropriate error codes.
package postgres

import (
	"github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PostgreSQL error codes (Class 23 — Integrity Constraint Violation).
const (
	// codeUniqueViolation is SQLSTATE 23505 (unique_violation).
	codeUniqueViolation = "23505"
	// codeForeignKeyViolation is SQLSTATE 23503 (foreign_key_violation).
	codeForeignKeyViolation = "23503"
	// codeCheckViolation is SQLSTATE 23514 (check_violation).
	codeCheckViolation = "23514"
	// codeNotNullViolation is SQLSTATE 23502 (not_null_violation).
	codeNotNullViolation = "23502"
	// codeExclusionViolation is SQLSTATE 23P01 (exclusion_violation).
	codeExclusionViolation = "23P01"
)

// PostgreSQL error codes (Class 42 — Syntax Error or Access Rule Violation).
const (
	// codeUndefinedTable is SQLSTATE 42P01 (undefined_table).
	codeUndefinedTable = "42P01"
	// codeUndefinedColumn is SQLSTATE 42703 (undefined_column).
	codeUndefinedColumn = "42703"
)

// PostgreSQL error codes (Class 40 — Transaction Rollback). Reachable since the
// native row-locking paths (SELECT … FOR UPDATE / SKIP LOCKED, e.g. a guard row or
// an outbox claim) can abort a transaction. Both
// are transient: the caller can retry the whole transaction and succeed.
const (
	// codeSerializationFailure is SQLSTATE 40001 (serialization_failure) — transient.
	codeSerializationFailure = "40001"
	// codeDeadlockDetected is SQLSTATE 40P01 (deadlock_detected) — transient.
	codeDeadlockDetected = "40P01"
)

// MapDBError maps PostgreSQL errors to core domain errors.
// Returns nil if the input error is nil.
func MapDBError(err error) error {
	if err == nil {
		return nil
	}

	// An error already classified upstream (an *AppError with a code) is returned
	// unchanged — re-wrapping it as Internal below would bury its code. Raw driver
	// errors are CodeUnknown here and still flow to the classification below. A
	// consumer's own ORM mapper (a DBMapperFunc) should keep the same rule.
	if errors.Code(err) != errors.CodeUnknown {
		return err
	}

	if errors.StdIs(err, pgx.ErrNoRows) {
		return errors.Wrap(err, errors.CodeNotFound, "record not found")
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return mapPgError(pgErr)
	}

	return errors.Wrap(err, errors.CodeInternal, "database error")
}

// mapPgError translates a pgconn.PgError to an AppError based on the
// PostgreSQL error code class.
func mapPgError(pgErr *pgconn.PgError) error {
	switch pgErr.Code {
	case codeUniqueViolation:
		return errors.Wrap(pgErr, errors.CodeConflict, pgErr.Message)
	case codeForeignKeyViolation:
		return errors.Wrap(
			pgErr,
			errors.CodeInvalidInput,
			"foreign key violation: "+pgErr.Message,
		)
	case codeCheckViolation:
		return errors.Wrap(
			pgErr,
			errors.CodeInvalidInput,
			"check violation: "+pgErr.Message,
		)
	case codeNotNullViolation:
		return errors.Wrap(
			pgErr,
			errors.CodeInvalidInput,
			"not-null violation: "+pgErr.Message,
		)
	case codeExclusionViolation:
		return errors.Wrap(
			pgErr,
			errors.CodeConflict,
			"exclusion violation: "+pgErr.Message,
		)
	case codeUndefinedTable, codeUndefinedColumn:
		return errors.Wrap(pgErr, errors.CodeInternal, "schema error: "+pgErr.Message)
	case codeSerializationFailure, codeDeadlockDetected:
		// Transient transaction rollback — the caller can retry the transaction.
		// Coded Conflict (not Internal) so a client sees a retryable 409 instead of a
		// terminal 500 on a serialization failure / deadlock under FOR UPDATE.
		return errors.Wrap(
			pgErr,
			errors.CodeConflict,
			"transaction conflict (retryable): "+pgErr.Message,
		)
	default:
		return errors.Wrap(pgErr, errors.CodeInternal, "database error: "+pgErr.Message)
	}
}
