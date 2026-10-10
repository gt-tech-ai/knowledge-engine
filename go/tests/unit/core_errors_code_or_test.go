package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	coreerr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// TestCodeOr_KeepsCodeElseFallsBack tests the code-with-fallback extractor.
//
// Why this test is important:
//   - Wrapping a client error must keep a code the client already chose (a
//     NOT_FOUND must not become UNAVAILABLE) while still coding an uncoded SDK
//     error, or the transport maps it to the wrong status
//
// What it tests:
//   - A CONFLICT error, also when wrapped, yields CONFLICT
//   - An uncoded error and a nil error yield the fallback
func TestCodeOr_KeepsCodeElseFallsBack(t *testing.T) {
	t.Parallel()
	conflict := coreerr.New(coreerr.CodeConflict, "dup")

	assert.Equal(
		t,
		coreerr.CodeConflict,
		coreerr.CodeOr(conflict, coreerr.CodeUnavailable),
	)
	assert.Equal(
		t,
		coreerr.CodeConflict,
		coreerr.CodeOr(
			coreerr.Wrap(conflict, coreerr.CodeConflict, "ctx"),
			coreerr.CodeUnavailable,
		),
	)
	assert.Equal(
		t,
		coreerr.CodeUnavailable,
		coreerr.CodeOr(coreerr.Sentinel("sdk"), coreerr.CodeUnavailable),
	)
	assert.Equal(t, coreerr.CodeInternal, coreerr.CodeOr(nil, coreerr.CodeInternal))
}
