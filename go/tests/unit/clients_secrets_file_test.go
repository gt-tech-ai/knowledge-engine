package unit_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/secrets/file"
	coreerr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/mocks"
)

// TestFileSource_PutThenGetRoundTripsOnIgnoredPath tests that the file Source writes a
// credential to a git-ignored file and reads the same value back.
//
// Why this test is important:
//   - file is the read/write backend for locally-provisioned credentials; a broken
//     write→read roundtrip would silently drop or corrupt a stored credential.
//
// What it tests:
//   - After git check-ignore confirms the path is ignored (runner returns nil), Put
//     persists the value and Get reveals it.
func TestFileSource_PutThenGetRoundTripsOnIgnoredPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)
	// `git check-ignore -q <path>` exits 0 when the path IS ignored → Run returns nil.
	runner.EXPECT().
		Run(gomock.Any(), gomock.Any(), "git", "check-ignore", "-q", gomock.Any()).
		Return(nil)

	path := filepath.Join(t.TempDir(), ".secrets.env")
	ref := types.Ref{Env: "dev", Class: "bootstrap", Field: "client_secret"}
	src := file.New(
		map[types.Ref]file.Location{ref: {Path: path, Key: "CLIENT_SECRET"}},
		runner,
	)

	require.NoError(t, src.Put(context.Background(), ref, types.NewSecret("wrote-me")))

	got, err := src.Get(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(t, "wrote-me", got.Reveal())
}

// TestFileSource_PutRefusesNonIgnoredPath tests that the file Source refuses to write a
// credential to a path git does not ignore, and writes nothing.
//
// Why this test is important:
//   - This is the guard that prevents a secret from being committed. If Put wrote to a
//     tracked path, the next commit would leak the credential — the exact failure the
//     backend exists to prevent.
//
// What it tests:
//   - When git check-ignore reports not-ignored (runner returns an error), Put returns
//     CodeInvalidInput and no file is created.
func TestFileSource_PutRefusesNonIgnoredPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)
	// Not ignored → check-ignore exits 1 → the CommandRunner returns a non-nil error.
	runner.EXPECT().
		Run(gomock.Any(), gomock.Any(), "git", "check-ignore", "-q", gomock.Any()).
		Return(errors.New("exit status 1"))

	path := filepath.Join(t.TempDir(), "tracked.env")
	ref := types.Ref{Env: "dev", Class: "bootstrap", Field: "client_secret"}
	src := file.New(
		map[types.Ref]file.Location{ref: {Path: path, Key: "CLIENT_SECRET"}},
		runner,
	)

	err := src.Put(context.Background(), ref, types.NewSecret("must-not-write"))
	require.True(t, coreerr.Is(err, coreerr.CodeInvalidInput))

	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "must not create a non-git-ignored credential file")
}

// TestFileSource_GetMissingKeyIsNotFound tests that Get for a key absent from the file
// yields a coded not-found error.
//
// Why this test is important:
//   - A missing key must be an explicit not-found, not an empty Secret a caller could
//     mistake for a real credential.
//
// What it tests:
//   - Get for a Ref whose key is not present in the file returns CodeNotFound (and Get
//     never invokes the runner).
func TestFileSource_GetMissingKeyIsNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".secrets.env")
	require.NoError(t, os.WriteFile(path, []byte("OTHER=x\n"), 0o600))

	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl) // Get must not call the runner.
	ref := types.Ref{Env: "dev", Class: "bootstrap", Field: "client_secret"}
	src := file.New(
		map[types.Ref]file.Location{ref: {Path: path, Key: "CLIENT_SECRET"}},
		runner,
	)

	_, err := src.Get(context.Background(), ref)
	require.True(t, coreerr.Is(err, coreerr.CodeNotFound))
}
