package unit_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine/go/foundation/golang"
)

// TestLintFilters tests the lint/format target filters.
//
// Why this test is important:
//   - These filters decide which modules/packages the lint, format, and typecheck
//     fan-outs touch; a wrong filter would lint generated code (noise/failures) or
//     silently skip first-party code.
//
// What it tests:
//   - IsLintTarget/GoLintFilter exclude gen/ and include everything else;
//     PythonLintFilter excludes the generated generated package.
func TestLintFilters(t *testing.T) {
	t.Parallel()
	assert.True(t, golang.IsLintTarget("pkg/go/core"))
	assert.False(t, golang.IsLintTarget("gen/go/pb"), "generated code excluded")
	assert.True(t, golang.GoLintFilter("apps/go/server/api"))
	assert.False(t, golang.GoLintFilter("gen/go/ent"))
	assert.True(t, golang.PythonLintFilter("pkg/python/techai_webutils"))
	assert.False(t, golang.PythonLintFilter("gen/python/generated"),
		"generated Python package excluded")
}

// TestPlatformPredicates tests the race/platform capability predicates.
//
// Why this test is important:
//   - The test runner toggles -race on these; if IsRaceSupported were wrong on
//     windows/arm64 (the one unsupported target) the test job would fail to start.
//
// What it tests:
//   - IsWindowsARM64 matches the current GOOS/GOARCH, and IsRaceSupported is its
//     inverse (race is supported everywhere except windows/arm64).
func TestPlatformPredicates(t *testing.T) {
	t.Parallel()
	winArm := runtime.GOOS == "windows" && runtime.GOARCH == "arm64"
	assert.Equal(t, winArm, golang.IsWindowsARM64())
	assert.Equal(t, !winArm, golang.IsRaceSupported(),
		"race is unsupported only on windows/arm64")
}

// TestFilterModulesWithGoPackages tests that modules without loadable Go
// packages are dropped while order is preserved.
//
// Why this test is important:
//   - The fan-out skips empty/non-Go modules; if it kept them, `go test`/`go vet`
//     would error on directories with no packages, failing the whole run.
//
// What it tests:
//   - Using the HasGoPackages stub seam, modules reporting no packages are dropped
//     and the surviving modules keep their original order.
func TestFilterModulesWithGoPackages(t *testing.T) {
	// Not parallel: mutates the package-level HasGoPackages stub seam.
	orig := golang.HasGoPackages
	t.Cleanup(func() { golang.HasGoPackages = orig })

	golang.HasGoPackages = func(dir string) bool { return !strings.HasSuffix(dir, "empty") }

	got := golang.FilterModulesWithGoPackages("/root", []string{"a", "empty", "b"})
	assert.Equal(t, []string{"a", "b"}, got, "empty module dropped, order preserved")
}

// TestHasGoPackages tests the real go-list probes against controlled dirs.
//
// Why this test is important:
//   - HasGoPackages/HasGoPackagesWithTags are the seam implementations the fan-out
//     filter depends on; a wrong answer (false negative) would silently skip a real
//     module from testing/linting.
//
// What it tests:
//   - An empty directory reports no Go packages; a directory with a go.mod + a Go
//     file reports packages (with and without build tags).
func TestHasGoPackages(t *testing.T) {
	// Not parallel: sets GOWORK/GOTOOLCHAIN so go list treats the temp dir as a
	// standalone module rather than complaining it is outside the repo workspace.
	t.Setenv("GOWORK", "off")
	t.Setenv("GOTOOLCHAIN", "local")

	assert.False(t, golang.HasGoPackages(t.TempDir()), "empty dir has no Go packages")

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.26.5\n"), 0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "x.go"), []byte("package x\n"), 0o644,
	))
	assert.True(t, golang.HasGoPackages(dir), "module with a Go file has packages")
	assert.True(t, golang.HasGoPackagesWithTags(dir, "integration"),
		"package is loadable with build tags too")
}

// TestFilterModulesWithGoPackages_KeepsLoadBrokenModule tests that a module whose
// `go list ./...` FAILS to load (a package-name conflict or build error) is KEPT in
// the discovery set, not silently dropped.
//
// Why this test is important:
//   - The Go test/lint/vet fan-outs discover modules through this filter. A prior
//     bug classified a module whose `go list` errored as "no Go packages" and dropped
//     it, so a package that failed to COMPILE was silently skipped while the gate
//     still reported PASS — a build break sailing through preflight/CI. A real module
//     that fails to load must be kept so the downstream tool fails loudly.
//
// What it tests:
//   - A real temp module (has go.mod) with two conflicting external test packages in
//     one subdir — which makes `go list ./...` fail — is retained by
//     FilterModulesWithGoPackages.
func TestFilterModulesWithGoPackages_KeepsLoadBrokenModule(t *testing.T) {
	// Not parallel: drives the real `go list` subprocess with GOWORK/GOTOOLCHAIN set.
	t.Setenv("GOWORK", "off")
	t.Setenv("GOTOOLCHAIN", "local")

	root := t.TempDir()
	const broken = "broken"
	dir := filepath.Join(root, broken)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "unit"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "go.mod"), []byte("module broken\n\ngo 1.26.5\n"), 0o644,
	))
	// Two external test packages in one dir make `go list ./...` fail to load.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "unit", "a_test.go"), []byte("package a_test\n"), 0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "unit", "b_test.go"), []byte("package b_test\n"), 0o644,
	))

	got := golang.FilterModulesWithGoPackages(root, []string{broken})
	assert.Equal(t, []string{broken}, got,
		"a load-broken module must be kept so its build error surfaces, not dropped")
}
