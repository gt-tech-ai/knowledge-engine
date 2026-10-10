package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	coreerr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// TestContextCode_MapsContextErrors tests the context-aware code extractor.
//
// Why this test is important:
//   - A cancelled or timed-out wait is returned by many decorators; coding it as
//     TIMEOUT or CANCELED keeps the retry policy (IsTransient) and the transport
//     status right instead of collapsing both into one generic code
//
// What it tests:
//   - An uncoded context.DeadlineExceeded (also inside a chain) yields TIMEOUT
//   - An uncoded context.Canceled yields CANCELED
//   - An already-coded error keeps its code; any other error yields the fallback
func TestContextCode_MapsContextErrors(t *testing.T) {
	t.Parallel()

	assert.Equal(
		t,
		coreerr.CodeTimeout,
		coreerr.ContextCode(context.DeadlineExceeded, coreerr.CodeInternal),
	)
	assert.Equal(
		t,
		coreerr.CodeTimeout,
		coreerr.ContextCode(
			coreerr.Join(coreerr.Sentinel("op"), context.DeadlineExceeded),
			coreerr.CodeInternal,
		),
	)
	assert.Equal(
		t,
		coreerr.CodeCanceled,
		coreerr.ContextCode(context.Canceled, coreerr.CodeInternal),
	)
	assert.Equal(
		t,
		coreerr.CodeConflict,
		coreerr.ContextCode(
			coreerr.Wrap(context.Canceled, coreerr.CodeConflict, "dup"),
			coreerr.CodeInternal,
		),
	)
	assert.Equal(
		t,
		coreerr.CodeUnavailable,
		coreerr.ContextCode(coreerr.Sentinel("sdk"), coreerr.CodeUnavailable),
	)
}
