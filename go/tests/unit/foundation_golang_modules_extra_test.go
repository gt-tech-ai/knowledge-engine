package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine/go/foundation/golang"
)

// TestIsRaceSupported_NoPanic tests that the race-detector capability probe
// returns a value on the current platform without panicking.
//
// Why this test is important:
//   - The test runner gates the `-race` flag on this result; a panic here would
//     break every Go test invocation that consults it before running tests.
//
// What it tests:
//   - IsRaceSupported is callable and returns without error on any host.
func TestIsRaceSupported_NoPanic(t *testing.T) {
	t.Parallel()
	_ = golang.IsRaceSupported()
}

// TestIsWindowsARM64_NoPanic tests that the windows/arm64 platform probe is
// callable on the current host without panicking.
//
// Why this test is important:
//   - This predicate drives platform-specific carve-outs (windows/arm64 lacks
//     -race); a panic would break tooling on every supported OS/arch.
//
// What it tests:
//   - IsWindowsARM64 executes and returns without error on any host.
func TestIsWindowsARM64_NoPanic(t *testing.T) {
	t.Parallel()
	_ = golang.IsWindowsARM64()
}

// TestIsLintTarget tests that generated modules under gen/ are excluded from
// lint/format targeting while first-party modules are included.
//
// Why this test is important:
//   - Linting machine-generated code (gen/go/pb, gen/go/ent) produces noise and
//     spurious failures; the gate must reliably skip those paths.
//
// What it tests:
//   - gen/-prefixed paths return false; pkg/ and apps/ paths return true.
func TestIsLintTarget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		{"pkg/go/core", true},
		{"gen/go/pb", false},
		{"gen/go/ent", false},
		{"apps/go/server", true},
	}
	for _, tc := range cases {
		assert.Equal(
			t,
			tc.want,
			golang.IsLintTarget(tc.path),
			"IsLintTarget(%q)",
			tc.path,
		)
	}
}

// TestFilterModulesWithGoPackages_FiltersEmpty tests that modules with no
// loadable Go packages are dropped while preserving the order of the rest.
//
// Why this test is important:
//   - Running go tooling (vet, test, build) against a package-less module dir
//     errors out; the filter keeps the gate from issuing those doomed commands.
//
// What it tests:
//   - With HasGoPackages stubbed to reject one dir, only the package-bearing
//     module survives, retaining input order.
func TestFilterModulesWithGoPackages_FiltersEmpty(t *testing.T) {
	// Stub HasGoPackages to avoid subprocess calls.
	orig := golang.HasGoPackages
	golang.HasGoPackages = func(dir string) bool { return dir != "/root/gen/go/ent" }
	t.Cleanup(func() { golang.HasGoPackages = orig })

	modules := []string{"pkg/go/core", "gen/go/ent"}
	got := golang.FilterModulesWithGoPackages("/root", modules)
	assert.Equal(
		t,
		[]string{"pkg/go/core"},
		got,
		"FilterModulesWithGoPackages = %v, want [pkg/go/core]",
		got,
	)
}

// TestPythonLintFilter tests that generated Python packages (suffix
// generated) are excluded from lint/typecheck while hand-written ones
// are kept.
//
// Why this test is important:
//   - The generated proto-stub package fails ruff/basedpyright by design; linting
//     it would block the gate on code no human maintains.
//
// What it tests:
//   - Paths ending in generated return false; real packages return true.
func TestPythonLintFilter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		{"pkg/python/techai_webutils", true},
		{"apps/python/server/retrieval", true},
		{"gen/python/generated", false},
		{"apps/python/generated", false},
	}
	for _, tc := range cases {
		assert.Equal(
			t,
			tc.want,
			golang.PythonLintFilter(tc.path),
			"PythonLintFilter(%q)",
			tc.path,
		)
	}
}
