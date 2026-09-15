package unit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine/go/foundation/golang"
)

// hugeLine is longer than bufio.Scanner's default 64 KiB token limit, so scanning a
// go.work containing it forces a scanner error (bufio.ErrTooLong).
var hugeLine = strings.Repeat("a", 70_000)

// writeFixtureFile writes content to path, creating parent directories.
func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

// TestGoWorkModules_ScannerError tests that a go.work whose use block cannot be
// scanned surfaces a wrapped read error.
//
// Why this test is important:
//   - GoWorkModules drives every scoped-workspace command; a corrupt or pathological
//     go.work must fail loudly with a read error rather than silently returning a
//     truncated module list that skips modules from lint/test.
//
// What it tests:
//   - A go.work with an over-long line inside the use block makes GoWorkModules return
//     a non-nil error.
func TestGoWorkModules_ScannerError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFixtureFile(
		t,
		filepath.Join(root, "go.work"),
		"go 1.26.5\n\nuse (\n"+hugeLine+"\n)\n",
	)

	_, err := golang.GoWorkModules(root, nil)
	require.Error(t, err)
}

// TestPythonPackages_Errors tests the failure and skip branches of Python package
// discovery.
//
// Why this test is important:
//   - PythonPackages feeds `search lint/typecheck/test --python`; a search directory
//     that is unreadable (or a nested tree that cannot be walked) must surface an
//     error, and stray non-directory entries must be skipped, so discovery neither
//     crashes nor silently drops packages.
//
// What it tests:
//   - A non-directory entry in a search dir is skipped while a real package is found.
//   - A search dir that is a file (not a directory) returns a read error.
//   - An unreadable nested directory propagates a walk error.
func TestPythonPackages_Errors(t *testing.T) {
	t.Parallel()

	t.Run("skips non-directory entries and finds packages", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		// A stray file alongside a real package dir under pkg/python.
		writeFixtureFile(t, filepath.Join(root, "pkg", "python", "stray.txt"), "x")
		writeFixtureFile(
			t,
			filepath.Join(root, "pkg", "python", "mypkg", "pyproject.toml"),
			"[project]\n",
		)

		pkgs, err := golang.PythonPackages(root, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{filepath.Join("pkg", "python", "mypkg")}, pkgs)
	})

	t.Run("search dir that is a file returns an error", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		// tools is one of the search dirs; make it a file so ReadDir fails (ENOTDIR),
		// a non-IsNotExist error that must propagate.
		writeFixtureFile(t, filepath.Join(root, "tools"), "not a dir")

		_, err := golang.PythonPackages(root, nil)
		require.Error(t, err)
	})

	t.Run("unreadable nested directory propagates a walk error", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		// mypkg has no pyproject, so discovery recurses into its subdir; make that
		// subdir unreadable so the recursive ReadDir fails and the error propagates.
		bad := filepath.Join(root, "pkg", "python", "mypkg", "sub")
		require.NoError(t, os.MkdirAll(bad, 0o755))
		require.NoError(t, os.Chmod(bad, 0o000))
		t.Cleanup(
			func() { _ = os.Chmod(bad, 0o755) },
		) // restore so TempDir cleanup can remove it

		_, err := golang.PythonPackages(root, nil)
		require.Error(t, err)
	})
}

// TestScopedEnv_ReadGoVersionErrors tests that ScopedEnv fails when it cannot read the
// go version from the root go.work.
//
// Why this test is important:
//   - ScopedEnv writes a temporary go.work whose `go` directive must match the repo's;
//     if the source go.work is missing or unreadable the scoped run would otherwise
//     produce an invalid workspace, so it must fail up front.
//
// What it tests:
//   - A root with no go.work makes ScopedEnv return an error (open failure).
//   - A go.work whose first line exceeds the scanner limit makes ScopedEnv return an
//     error (scanner failure while reading the go version).
func TestScopedEnv_ReadGoVersionErrors(t *testing.T) {
	t.Parallel()

	t.Run("missing go.work", func(t *testing.T) {
		t.Parallel()
		_, cleanup, err := golang.ScopedEnv(t.TempDir(), "pkg/go/core")
		t.Cleanup(cleanup)
		require.Error(t, err)
	})

	t.Run("unscannable go.work", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeFixtureFile(t, filepath.Join(root, "go.work"), hugeLine+"\n")

		_, cleanup, err := golang.ScopedEnv(root, "pkg/go/core")
		t.Cleanup(cleanup)
		require.Error(t, err)
	})
}

// TestHasGoPackagesWithTags_ListFailure tests that the build-tag probe reports false
// for a non-module directory `go list` cannot load.
//
// Why this test is important:
//   - HasGoPackagesWithTags gates whether a scoped workspace includes a module for a
//     tagged build. A non-module dir (no go.mod) that go list cannot load is not ours
//     to test and must read as "no packages" rather than crash the caller. (A real
//     module whose code fails to load is instead kept — see hasGoPackagesWithTags.)
//
// What it tests:
//   - Probing an empty directory that is not a Go module returns false.
func TestHasGoPackagesWithTags_ListFailure(t *testing.T) {
	t.Parallel()
	assert.False(t, golang.HasGoPackagesWithTags(t.TempDir(), ""))
}
