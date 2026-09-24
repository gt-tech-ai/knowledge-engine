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

// wsWrite creates a file (with parent dirs) under a test temp root.
func wsWrite(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// TestGoWorkModules tests parsing the go.work use block into module paths.
//
// Why this test is important:
//   - Every test/lint/coverage fan-out enumerates modules from go.work; if the
//     parser mis-read the use block (kept ".", ignored the filter, or missed a
//     module) the CLI would test the wrong set of modules.
//
// What it tests:
//   - The use-block modules are returned relative to root with "." excluded; the
//     filter drops modules by relative path; a missing go.work returns an error.
func TestGoWorkModules(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	wsWrite(t, filepath.Join(root, "go.work"),
		"go 1.26.5\n\nuse (\n\t.\n\t./pkg/go/core\n\t./apps/api\n)\n")

	mods, err := golang.GoWorkModules(root, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"pkg/go/core", "apps/api"}, mods,
		"use-block modules returned, root '.' excluded")

	filtered, err := golang.GoWorkModules(root, func(rel string) bool {
		return !strings.HasPrefix(rel, "apps/")
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"pkg/go/core"}, filtered, "filter drops apps/ modules")

	_, err = golang.GoWorkModules(t.TempDir(), nil)
	require.Error(t, err, "missing go.work errors")
}

// TestPythonPackages tests discovery of Python packages by pyproject.toml.
//
// Why this test is important:
//   - The Python test/lint fan-out is driven by this discovery; a missed package
//     (or a false positive on a dir without pyproject.toml) would skip or break a
//     project's checks.
//
// What it tests:
//   - Directories containing pyproject.toml under pkg/python + apps/python are
//     returned relative to root — both directly and one level nested (exercising
//     the recursive descent that then appends); dirs without one are skipped; the
//     filter narrows the result; missing search dirs are tolerated.
func TestPythonPackages(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	wsWrite(
		t,
		filepath.Join(root, "pkg/python/lib/pyproject.toml"),
		"[project]\nname='lib'\n",
	)
	wsWrite(
		t,
		filepath.Join(root, "apps/python/svc/pyproject.toml"),
		"[project]\nname='svc'\n",
	)
	// Nested one level down: apps/python/server has no pyproject, so discovery must
	// recurse into server/ and only then find + append the deep package.
	wsWrite(
		t,
		filepath.Join(root, "apps/python/server/deep/pyproject.toml"),
		"[project]\nname='deep'\n",
	)
	wsWrite(t, filepath.Join(root, "pkg/python/nope/readme.md"), "no pyproject here")

	pkgs, err := golang.PythonPackages(root, nil)
	require.NoError(t, err)
	assert.ElementsMatch(
		t,
		[]string{"pkg/python/lib", "apps/python/svc", "apps/python/server/deep"},
		pkgs,
	)

	filtered, err := golang.PythonPackages(root, func(rel string) bool {
		return strings.HasPrefix(rel, "pkg/")
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"pkg/python/lib"}, filtered, "filter narrows to pkg/")
}

// TestScopedEnv tests the temporary scoped go.work used to run a subset of
// modules in isolation.
//
// Why this test is important:
//   - Scoped runs (e.g. per-service codegen/build) depend on a temp go.work that
//     lists only the needed modules with the right go version; a malformed file or
//     a leaked temp dir would break isolated builds or pollute the system.
//
// What it tests:
//   - ScopedEnv returns a GOWORK= var pointing at a temp go.work carrying the
//     root's go version and absolute use paths for the requested modules; the file
//     lives OUTSIDE the repo root; cleanup removes it; a go.work with no go
//     directive is an error.
func TestScopedEnv(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	wsWrite(t, filepath.Join(root, "go.work"), "go 1.26.5\n\nuse (\n\t.\n)\n")

	env, cleanup, err := golang.ScopedEnv(root, "pkg/go/core", "pkg/go/foundation")
	require.NoError(t, err)
	defer cleanup()

	workPath, ok := strings.CutPrefix(env, "GOWORK=")
	require.True(t, ok, "env var is GOWORK=<path>")
	// The scoped go.work must live OUTSIDE the repo root: a relative-path file had
	// to live in root, so an interrupted run (SIGKILL skips cleanup) left stray
	// .go.work.scoped-* files in the working tree. Placing it in a system temp dir
	// (with absolute use paths) keeps a crashed run from polluting the repository.
	assert.False(t, strings.HasPrefix(workPath, root),
		"scoped go.work must live outside root %q, got %q", root, workPath)
	data, err := os.ReadFile(workPath)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "go 1.26.5", "carries root go version")
	assert.Contains(t, content, filepath.Join(root, "pkg/go/core"), "absolute use path")
	assert.Contains(t, content, filepath.Join(root, "pkg/go/foundation"))

	cleanup()
	_, statErr := os.Stat(workPath)
	assert.True(t, os.IsNotExist(statErr), "cleanup removes the scoped go.work")

	// A go.work without a go directive cannot produce a valid scoped env.
	bad := t.TempDir()
	wsWrite(t, filepath.Join(bad, "go.work"), "use (\n\t.\n)\n")
	_, _, err = golang.ScopedEnv(bad, "pkg/go/core")
	require.Error(t, err, "no go directive -> error")
}
