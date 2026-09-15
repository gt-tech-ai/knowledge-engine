package unit_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine/go/foundation/golang"
)

// writeGoModule writes a minimal standalone Go module (go.mod + one package file)
// into dir so `go list ./...` reports exactly one package.
func writeGoModule(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "go.mod"), []byte("module memo_x\n\ngo 1.26.5\n"), 0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "x.go"), []byte("package memo_x\n"), 0o644,
	))
}

// TestGoListPackages_MemoizesAndCachesErrors tests that GoListPackages runs
// `go list` at most once per directory and serves every later call — including
// concurrent ones — from the cached result, and that a `go list` failure is
// cached too.
//
// Why this test is important:
//   - The preflight gate probes eligible Go modules ~5x per run; without the memo
//     each phase reruns `go list ./...` per module, a subprocess storm. The cache
//     must also survive concurrent probes and must remember failures so an
//     unlistable dir is not retried every phase.
//   - HasGoPackages reads the same memo, so a regression here silently
//     reintroduces duplicate subprocess calls and mis-classifies modules.
//
// What it tests (entirely through the public GoListPackages/HasGoPackages API):
//   - The result is memoized: after the first call the directory's contents are
//     removed, yet a second call returns the SAME package list — proving `go list`
//     did not re-run, since a re-run against the emptied dir would differ. This is
//     the black-box equivalent of counting the underlying runner's invocations.
//   - Concurrent first-touch calls for one dir all observe a single consistent
//     cached result (the per-dir sync.Once dedupe).
//   - A `go list` failure is cached: a dir that first fails keeps returning the
//     error even after it is made valid, and HasGoPackages reports false for a
//     non-module dir (no go.mod) that cannot be listed.
func TestGoListPackages_MemoizesAndCachesErrors(t *testing.T) {
	// Not parallel: sets GOWORK/GOTOOLCHAIN and drives the real `go list`
	// subprocess. GOWORK=off makes go list treat the temp dir as a standalone
	// module rather than complaining it sits outside the repo workspace.
	t.Setenv("GOWORK", "off")
	t.Setenv("GOTOOLCHAIN", "local")

	// --- success + memoization (proved by the cache surviving dir mutation) ---
	okDir := t.TempDir()
	writeGoModule(t, okDir)

	// Concurrent first-touch calls must all agree; the per-dir sync.Once collapses
	// them to a single `go list`. assert (not require) inside goroutines: require's
	// FailNow is only legal on the test goroutine.
	const goroutines = 20
	results := make([][]string, goroutines)
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			pkgs, err := golang.GoListPackages(okDir)
			assert.NoError(t, err)
			results[idx] = pkgs
		}(i)
	}
	wg.Wait()

	first, err := golang.GoListPackages(okDir)
	require.NoError(t, err)
	require.NotEmpty(t, first, "a valid module must report at least one package")
	for i, r := range results {
		assert.Equal(t, first, r, "concurrent call %d saw a different result", i)
	}

	// Removing the directory contents would change what `go list` reports; the memo
	// must return the ORIGINAL result, proving the subprocess did not run again.
	require.NoError(t, os.RemoveAll(okDir))
	cached, err := golang.GoListPackages(okDir)
	require.NoError(t, err, "cached success must survive dir removal")
	assert.Equal(t, first, cached, "second call must return the memoized package list")

	// --- error caching ---
	badDir := filepath.Join(t.TempDir(), "missing") // never created -> go list fails
	_, errFirst := golang.GoListPackages(badDir)
	require.Error(t, errFirst, "listing a non-existent dir must fail")
	assert.False(t, golang.HasGoPackages(badDir),
		"a non-module dir (no go.mod) that go list cannot load reports no packages")

	// Make the dir valid; the cached error must still be served (no retry).
	require.NoError(t, os.MkdirAll(badDir, 0o755))
	writeGoModule(t, badDir)
	_, errSecond := golang.GoListPackages(badDir)
	require.Error(t, errSecond, "cached error must survive the dir becoming valid")
	assert.Equal(t, errFirst.Error(), errSecond.Error(),
		"the same cached error must be returned, not a fresh listing")
}
