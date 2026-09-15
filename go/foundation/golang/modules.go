package golang

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
)

// isGoModule reports whether dir is a Go module root (contains a go.mod). It
// disambiguates a `go list` failure: a load error inside a real module means the
// module's code is broken (keep it so the tool fails loudly), whereas a non-module
// directory is simply not ours to test (drop it).
func isGoModule(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil
}

// IsLintTarget returns true for modules that should be linted/formatted.
// Generated code (gen/) is excluded since it's machine-produced.
func IsLintTarget(relPath string) bool {
	return !strings.HasPrefix(relPath, "gen/")
}

// GoLintFilter is an alias for IsLintTarget for use as a discovery filter
// function parameter.
var GoLintFilter = IsLintTarget

// PythonLintFilter excludes generated Python packages from lint/typecheck runs.
var PythonLintFilter = func(relPath string) bool {
	return !strings.HasSuffix(relPath, "generated")
}

// IsRaceSupported reports whether the Go race detector is available on the
// current OS/architecture. Notably, windows/arm64 does not support -race.
func IsRaceSupported() bool {
	return runtime.GOOS != "windows" || runtime.GOARCH != "arm64"
}

// IsWindowsARM64 reports whether the current platform is Windows on ARM64.
func IsWindowsARM64() bool {
	return runtime.GOOS == "windows" && runtime.GOARCH == "arm64"
}

// HasGoPackages reports whether the module at dir contains at least one Go
// package loadable under default build constraints.
//
// Declared as a variable so unit tests can stub out the subprocess call.
var HasGoPackages = hasGoPackages

// hasGoPackages reports whether the module at dir contains loadable Go packages.
// It reads the memoized GoListPackages, so the `go list ./...` for a given dir
// runs at most once per process regardless of how many phases probe it.
func hasGoPackages(dir string) bool {
	pkgs, err := GoListPackages(dir)
	if err != nil {
		// A `go list ./...` error inside a real module (has go.mod) means the
		// module's Go code fails to load — a package-name conflict or build error —
		// NOT that it has no packages. Report it PRESENT so the module stays in the
		// discovery set and the downstream tool (go test / vet / lint) runs and fails
		// LOUDLY on the real error. Returning false here silently DROPS the module,
		// which is exactly how a non-compiling package once sailed through preflight
		// reporting PASS. A non-module dir is not ours to test.
		return isGoModule(dir)
	}
	return len(pkgs) > 0
}

// goListRunner runs `go list ./...` in dir and returns the package import paths
// (one per line, whitespace-split). It is a var so the memo's instrumented test
// can swap it to count invocations without spawning a subprocess.
var goListRunner = func(dir string) ([]string, error) {
	cmd := exec.Command(
		"go",
		"list",
		"./...",
	) //nolint:gosec // trusted internal command, hardcoded binary
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

// goListResult memoizes one directory's `go list ./...` outcome.
type goListResult struct {
	// err is the error from the memoized `go list ./...`, if it failed.
	err error
	// pkgs is the list of package import paths returned by `go list ./...`.
	pkgs []string
}

var (
	// goListMu guards the two memo maps (held only for map access, never across
	// the subprocess, so distinct dirs still list concurrently).
	goListMu sync.Mutex
	// goListOnce ensures each dir's listing runs exactly once.
	goListOnce = map[string]*sync.Once{}
	// goListCache holds each dir's completed listing.
	goListCache = map[string]goListResult{}
)

// GoListPackages returns the Go package import paths under dir (`go list ./...`),
// memoized per directory for the process lifetime. The CLI runs one command per
// process and the workspace is stable within a run, so caching collapses the
// repeated `go list` storm — module-eligibility probing runs ~5x per preflight
// (format, lint, typecheck, security, test), plus the per-integration-module
// unit-package exclusion — down to a single `go list` per directory (#5/#24).
// Both FilterModulesWithGoPackages (eligibility) and the CLI's unit-package
// exclusion read it, so they share one listing per dir. Concurrent calls for the
// same dir dedupe on a per-dir sync.Once; distinct dirs list in parallel.
func GoListPackages(dir string) ([]string, error) {
	goListMu.Lock()
	once, ok := goListOnce[dir]
	if !ok {
		once = &sync.Once{}
		goListOnce[dir] = once
	}
	goListMu.Unlock()

	once.Do(func() {
		pkgs, err := goListRunner(dir)
		goListMu.Lock()
		goListCache[dir] = goListResult{pkgs: pkgs, err: err}
		goListMu.Unlock()
	})

	goListMu.Lock()
	defer goListMu.Unlock()
	r := goListCache[dir]
	return r.pkgs, r.err
}

// HasGoPackagesWithTags reports whether the module at dir contains at least one
// Go package loadable under the specified build tags.
//
// Declared as a variable so unit tests can stub out the subprocess call.
var HasGoPackagesWithTags = hasGoPackagesWithTags

// hasGoPackagesWithTags reports whether the module at dir contains Go packages loadable with the given build tags.
func hasGoPackagesWithTags(dir, tags string) bool {
	cmd := exec.Command( //nolint:gosec // trusted internal command with hardcoded binary
		"go",
		"list",
		"-tags="+tags,
		"./...",
	)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		// A `go list` error inside a real module means its Go code fails to load (a
		// package conflict or build error) — report it present so the module is kept
		// and the tagged build fails loudly rather than silently dropping a build
		// break (mirrors hasGoPackages). A non-module dir is not ours to include.
		return isGoModule(dir)
	}
	return len(out) > 0 && string(out) != "\n"
}

// ProtoModules lists the relative module directories required for a scoped
// workspace when running proto code generation (buf generate + go mod tidy).
var ProtoModules = []string{"contracts/proto", "gen/go/pb"}

// EntModules lists the relative module directories required for a scoped
// workspace when running Ent code generation (entc + go mod tidy).
var EntModules = []string{"contracts/ent", "gen/go/ent"}

// FilterModulesWithGoPackages concurrently checks which modules contain
// loadable Go packages and returns the filtered list preserving order.
func FilterModulesWithGoPackages(
	root string,
	modules []string,
) []string {
	ok := make([]bool, len(modules))
	// Bound concurrency at NumCPU with errgroup.SetLimit (acquire-before-spawn),
	// so a workspace with many modules holds ≤NumCPU probe goroutines rather than
	// one per module (#4).
	g := new(errgroup.Group)
	g.SetLimit(runtime.NumCPU())
	for i, mod := range modules {
		idx, dir := i, filepath.Join(root, mod)
		g.Go(func() error {
			ok[idx] = HasGoPackages(dir)
			return nil
		})
	}
	_ = g.Wait()

	filtered := make([]string, 0, len(modules))
	for i, mod := range modules {
		if ok[i] {
			filtered = append(filtered, mod)
		}
	}
	return filtered
}
