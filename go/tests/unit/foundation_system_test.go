package unit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine/go/foundation/system"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/mocks"
)

// TestRunner_RealCommands tests the real Runner against the always-present `go`
// binary and a missing binary.
//
// Why this test is important:
//   - Runner executes every external tool the CLI shells out to; if it
//     mis-reported success/failure or exit codes, every gate's pass/fail signal
//     would be wrong. Exercising it against real commands proves the behavior.
//
// What it tests:
//   - Run succeeds on a real command and errors on a missing binary; RunBuffered
//     captures stdout and exit 0 for success, and exit -1 (never started) for a
//     missing binary; the *WithEnv variants run the extra-env path.
func TestRunner_RealCommands(t *testing.T) {
	t.Parallel()
	r := system.NewRunner()
	ctx := context.Background()

	require.NoError(t, r.Run(ctx, "", "go", "version"))
	require.Error(t, r.Run(ctx, "", "definitely-not-a-real-binary-xyz"))

	res := r.RunBuffered(ctx, "", "go", "version")
	assert.NoError(t, res.Err)
	assert.Equal(t, 0, res.ExitCode)
	assert.Contains(t, string(res.Stdout), "go version")

	bad := r.RunBuffered(ctx, "", "definitely-not-a-real-binary-xyz")
	assert.Error(t, bad.Err)
	assert.Equal(t, -1, bad.ExitCode, "missing binary never starts -> exit -1")

	require.NoError(t, r.RunWithEnv(ctx, "", []string{"FOO=bar"}, "go", "version"))
	envRes := r.RunBufferedWithEnv(ctx, "", []string{"FOO=bar"}, "go", "version")
	assert.NoError(t, envRes.Err)
}

// TestRunner_ExistsAndRequireTool tests PATH lookup and the tool-required guard.
//
// Why this test is important:
//   - RequireTool gates every command behind a clear "install X" error; a false
//     positive would let a gate run a missing tool and fail cryptically.
//
// What it tests:
//   - Exists is true for `go` and false for a missing binary; RequireTool returns
//     nil for a present tool and an error carrying the install hint for a missing one.
func TestRunner_ExistsAndRequireTool(t *testing.T) {
	t.Parallel()
	r := system.NewRunner()
	assert.True(t, r.Exists("go"))
	assert.False(t, r.Exists("definitely-not-a-real-binary-xyz"))

	require.NoError(t, r.RequireTool("go", "install go"))
	err := r.RequireTool("definitely-not-a-real-binary-xyz", "brew install xyz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "brew install xyz", "install hint surfaced")
}

// TestRunner_ContextCancelled tests that a cancelled context surfaces as an error.
//
// Why this test is important:
//   - Ctrl+C / timeouts cancel the context; Runner must abort and report rather
//     than hang or claim success.
//
// What it tests:
//   - Run with an already-cancelled context returns an error.
func TestRunner_ContextCancelled(t *testing.T) {
	t.Parallel()
	r := system.NewRunner()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, r.Run(ctx, "", "go", "version"), "cancelled context -> error")
}

// TestBufferingRunner tests that BufferingRunner captures inner output, wraps
// failures, and delegates the non-running methods.
//
// Why this test is important:
//   - BufferingRunner is what concurrent gates use so command output is captured
//     for failure diagnostics (StepResult.Detail) instead of interleaving on the
//     terminal; dropped capture would lose the diagnostics, and a swallowed inner
//     error would hide a failed step.
//
// What it tests:
//   - RunBuffered/RunBufferedWithEnv accumulate stdout+stderr into Captured();
//     Run returns nil on inner success and a wrapped error (naming the command) on
//     inner failure while still capturing output; RunWithEnv delegates to the
//     inner env path; Exists/RequireTool delegate; both quiet and replay
//     constructors work.
func TestBufferingRunner(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	inner := mocks.NewMockCommandRunner(ctrl)
	inner.EXPECT().
		RunBuffered(gomock.Any(), "", "x").
		Return(system.CmdResult{Stdout: []byte("out-"), Stderr: []byte("err")})
	inner.EXPECT().
		RunBufferedWithEnv(gomock.Any(), "", []string{"A=b"}, "x").
		Return(system.CmdResult{Stdout: []byte("out-"), Stderr: []byte("err")})
	inner.EXPECT().Exists("anything").Return(true)
	inner.EXPECT().RequireTool("x", "").Return(nil)
	b := system.NewQuietBufferingRunner(inner)

	res := b.RunBuffered(context.Background(), "", "x")
	assert.Equal(t, "out-", string(res.Stdout))
	assert.Equal(
		t,
		"out-err",
		string(b.Captured()),
		"RunBuffered accumulates stdout+stderr",
	)

	_ = b.RunBufferedWithEnv(context.Background(), "", []string{"A=b"}, "x")
	assert.Equal(t, "out-errout-err", string(b.Captured()))

	assert.True(t, b.Exists("anything"), "Exists delegates")
	assert.NoError(t, b.RequireTool("x", ""), "RequireTool delegates")

	// Run (no env) wraps a failing inner result and still captures its output.
	failingInner := mocks.NewMockCommandRunner(ctrl)
	failingInner.EXPECT().
		RunBuffered(gomock.Any(), "", "tool", "arg").
		Return(system.CmdResult{Stderr: []byte("boom"), Err: errors.New("exit 1")})
	failing := system.NewQuietBufferingRunner(failingInner)
	err := failing.Run(context.Background(), "", "tool", "arg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tool arg", "wrapped error names the command")
	assert.Equal(t, "boom", string(failing.Captured()))

	// Run (no env) success path.
	okInner := mocks.NewMockCommandRunner(ctrl)
	okInner.EXPECT().
		RunBuffered(gomock.Any(), "", "x").
		Return(system.CmdResult{Stdout: []byte("hi")})
	okRunner := system.NewQuietBufferingRunner(okInner)
	require.NoError(t, okRunner.Run(context.Background(), "", "x"))

	// RunWithEnv delegates to the inner env path.
	envInner := mocks.NewMockCommandRunner(ctrl)
	envInner.EXPECT().
		RunWithEnv(gomock.Any(), "", []string{"A=b"}, "x").
		Return(nil)
	envRunner := system.NewQuietBufferingRunner(envInner)
	require.NoError(
		t,
		envRunner.RunWithEnv(context.Background(), "", []string{"A=b"}, "x"),
	)

	// Non-quiet (replay) constructor exercises the terminal-replay branches.
	replayInner := mocks.NewMockCommandRunner(ctrl)
	replayInner.EXPECT().
		RunBuffered(gomock.Any(), "", "x").
		Return(system.CmdResult{Stdout: []byte("o"), Stderr: []byte("e")})
	replay := system.NewBufferingRunner(replayInner)
	require.NoError(t, replay.Run(context.Background(), "", "x"))

	replayFailInner := mocks.NewMockCommandRunner(ctrl)
	replayFailInner.EXPECT().
		RunBuffered(gomock.Any(), "", "x").
		Return(system.CmdResult{Err: errors.New("x")})
	replayFail := system.NewBufferingRunner(replayFailInner)
	require.Error(t, replayFail.Run(context.Background(), "", "x"))

	replayEnvInner := mocks.NewMockCommandRunner(ctrl)
	replayEnvInner.EXPECT().
		RunWithEnv(gomock.Any(), "", []string{"A=b"}, "x").
		Return(nil)
	replayEnv := system.NewBufferingRunner(replayEnvInner)
	require.NoError(
		t,
		replayEnv.RunWithEnv(context.Background(), "", []string{"A=b"}, "x"),
	)
}
