package unit_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"

	coreerr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/repos/errors/postgres"
)

// TestPostgresMapDBError_Classifies tests that the PostgreSQL error mapper assigns the
// correct domain error code by SQLSTATE class.
//
// Why this test is important:
//   - The mapped code drives the HTTP/gRPC status and the retry policy. A Class-40
//     transaction rollback (serialization failure / deadlock) is now reachable via the
//
// native row-locking paths added in (SELECT … FOR UPDATE / SKIP LOCKED);
//
//	it must classify as a RETRYABLE Conflict, not a terminal Internal, so a client
//	learns a retry would succeed. An already-coded error must pass through unchanged so
//	an upstream classification (e.g. a hook's Conflict) is not reburied as Internal.
//
// What it tests:
//   - 40001 (serialization_failure) and 40P01 (deadlock_detected) → CodeConflict.
//   - 23505 (unique_violation) → CodeConflict (existing behavior, guarded here too).
//   - nil → nil; an already-coded AppError → returned unchanged.
func TestPostgresMapDBError_Classifies(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		code string
		want coreerr.ErrorCode
	}{
		{"serialization failure is a retryable conflict", "40001", coreerr.CodeConflict},
		{"deadlock detected is a retryable conflict", "40P01", coreerr.CodeConflict},
		{"unique violation is a conflict", "23505", coreerr.CodeConflict},
		{"foreign key violation is invalid input", "23503", coreerr.CodeInvalidInput},
		{"undefined table is internal", "42P01", coreerr.CodeInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := postgres.MapDBError(&pgconn.PgError{Code: tc.code, Message: "x"})
			assert.Equal(t, tc.want, coreerr.Code(got))
		})
	}

	assert.Nil(t, postgres.MapDBError(nil), "nil in → nil out")

	// An already-coded error passes through unchanged (no re-wrap to Internal).
	coded := coreerr.New(coreerr.CodeNotFound, "already classified")
	assert.Same(t, error(coded), postgres.MapDBError(coded),
		"an already-coded AppError must be returned unchanged")
}
