package golang

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// GoWorkModules parses go.work and returns the module directories listed in
// the use block, relative to root (e.g. "pkg/go/core", "apps/go/server/api").
// The root module (".") is excluded. An optional filter excludes modules by
// relative path (return false to skip).
func GoWorkModules(root string, filter func(relPath string) bool) ([]string, error) {
	f, err := os.Open(filepath.Join(root, "go.work"))
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeNotFound, "open go.work")
	}
	defer func() { _ = f.Close() }()

	var modules []string
	inUseBlock := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "use (") || line == "use (" {
			inUseBlock = true
			continue
		}
		if inUseBlock && line == ")" {
			break
		}
		if !inUseBlock {
			continue
		}
		mod := strings.TrimPrefix(line, "./")
		if mod == "" || mod == "." {
			continue
		}
		if filter != nil && !filter(mod) {
			continue
		}
		modules = append(modules, mod)
	}
	if err := scanner.Err(); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "read go.work")
	}
	return modules, nil
}

// PythonPackages discovers Python packages by walking pkg/python/ and
// apps/python/ for directories containing a pyproject.toml. Paths are
// returned relative to root (e.g. "pkg/python/techai_webutils"). An optional
// filter excludes packages by relative path (return false to skip).
func PythonPackages(root string, filter func(relPath string) bool) ([]string, error) {
	searchDirs := []string{
		filepath.Join(root, "pkg", "python"),
		filepath.Join(root, "apps", "python"),
		// Python dev tools (e.g. tools/synthetic, the binary renderer) so they are covered by
		// `search lint/typecheck/test --python`. tools/cli (Go) has no pyproject, so it is a no-op there.
		filepath.Join(root, "tools"),
	}

	var packages []string
	for _, searchDir := range searchDirs {
		entries, err := os.ReadDir(searchDir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, apperr.Wrap(err, apperr.CodeInternal, "read directory "+searchDir)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			abs := filepath.Join(searchDir, entry.Name())
			if err := findPyprojectDirs(root, abs, filter, &packages); err != nil {
				return nil, err
			}
		}
	}
	return packages, nil
}

// findPyprojectDirs recursively finds directories containing pyproject.toml
// under dir and appends their paths (relative to root) to out.
func findPyprojectDirs(
	root, dir string,
	filter func(string) bool,
	out *[]string,
) error {
	if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); err == nil {
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "compute relative path")
		}
		if filter == nil || filter(rel) {
			*out = append(*out, rel)
		}
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "read directory "+dir)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := findPyprojectDirs(
			root,
			filepath.Join(dir, entry.Name()),
			filter,
			out,
		); err != nil {
			return err
		}
	}
	return nil
}

// ScopedEnv creates a temporary go.work file in a system temp directory listing
// only the given module directories (as absolute paths). It returns:
//   - envVar: "GOWORK=/absolute/path" for use in RunWithEnv/RunBufferedWithEnv
//   - cleanup: removes the temp directory (safe to call multiple times)
//   - err: non-nil if the temp file cannot be created
//
// The caller must defer cleanup() to avoid leaving temp files behind. The file
// lives outside the repository so an interrupted run cannot leave a stray
// .go.work.scoped-* in the working tree.
func ScopedEnv(
	root string,
	moduleDirs ...string,
) (envVar string, cleanup func(), err error) {
	goVersion, vErr := readGoVersion(root)
	if vErr != nil {
		return "", func() {}, apperr.Wrap(
			vErr,
			apperr.CodeInternal,
			"read go version from go.work",
		)
	}

	// Write the scoped go.work into a system temp directory rather than the repo
	// root: a relative-path go.work had to live in root, so an interrupted run
	// (SIGKILL skips the cleanup defer) left stray .go.work.scoped-* files in the
	// top-level working tree. The temp file uses absolute use paths so it resolves
	// the same from anywhere, and a crashed run leaks into the OS temp dir instead
	// of polluting the repository.
	tmpDir, dErr := os.MkdirTemp("", "ke-scoped-gowork-*")
	if dErr != nil {
		return "", func() {}, apperr.Wrap(
			dErr,
			apperr.CodeInternal,
			"create scoped go.work dir",
		)
	}
	cleanup = func() { _ = os.RemoveAll(tmpDir) }

	var b strings.Builder
	fmt.Fprintf(&b, "go %s\n\nuse (\n", goVersion)
	for _, dir := range moduleDirs {
		fmt.Fprintf(&b, "\t%s\n", filepath.Join(root, dir))
	}
	b.WriteString(")\n")

	workPath := filepath.Join(tmpDir, "go.work")
	if wErr := os.WriteFile(workPath, []byte(b.String()), 0o600); wErr != nil {
		cleanup()
		return "", func() {}, apperr.Wrap(
			wErr,
			apperr.CodeInternal,
			"write scoped go.work",
		)
	}

	return "GOWORK=" + workPath, cleanup, nil
}

// readGoVersion extracts the go version from the root go.work file.
func readGoVersion(root string) (string, error) {
	f, err := os.Open(filepath.Join(root, "go.work"))
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck // best-effort close on read-only file

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if v, ok := strings.CutPrefix(line, "go "); ok {
			return v, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", apperr.New(apperr.CodeInvalidInput, "no 'go' directive found in go.work")
}
