// Package system provides CommandRunner implementations for executing external
// commands. The canonical CommandRunner interface is defined in core/interfaces;
// this package provides concrete runners and decorator builders.
package system

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
)

// CommandRunner is a type alias for interfaces.CommandRunner.
// system.CommandRunner and interfaces.CommandRunner are identical types;
// no cast or conversion is needed at any call site.
type CommandRunner = interfaces.CommandRunner

// CmdResult is a type alias for interfaces.CmdResult.
type CmdResult = interfaces.CmdResult

// Compile-time interface compliance check.
var _ interfaces.CommandRunner = (*Runner)(nil)

// Runner implements CommandRunner using real command execution with inherited
// stdout/stderr so terminal output flows directly to the user.
type Runner struct{}

// NewRunner creates a Runner for real command execution.
func NewRunner() *Runner { return &Runner{} }

// Run executes a command with inherited stdout/stderr.
func (d *Runner) Run(ctx context.Context, dir, name string, args ...string) error {
	return d.runWithEnv(ctx, dir, nil, name, args...)
}

// RunWithEnv executes a command with extra environment variables appended.
func (d *Runner) RunWithEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) error {
	return d.runWithEnv(ctx, dir, extraEnv, name, args...)
}

// RunBuffered executes a command with stdout/stderr captured into buffers.
func (d *Runner) RunBuffered(
	ctx context.Context,
	dir, name string,
	args ...string,
) interfaces.CmdResult {
	return d.runBuffered(ctx, dir, nil, name, args...)
}

// RunBufferedWithEnv executes a command with extra environment variables
// appended and stdout/stderr captured into buffers.
func (d *Runner) RunBufferedWithEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) interfaces.CmdResult {
	return d.runBuffered(ctx, dir, extraEnv, name, args...)
}

// runBuffered is the shared implementation for RunBuffered and
// RunBufferedWithEnv.
func (d *Runner) runBuffered(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) interfaces.CmdResult {
	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(
		ctx,
		name,
		args...,
	) //nolint:gosec // caller-controlled args
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(), extraEnv...)
	configureProcessGroup(cmd)

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	exitCode := 0
	if err != nil {
		// ProcessState is set when the process ran and exited (incl. non-zero);
		// nil when it never started (e.g. binary not found) — report -1 then.
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		} else {
			exitCode = -1
		}
	}

	return interfaces.CmdResult{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		Duration: dur,
		Err:      err,
		ExitCode: exitCode,
	}
}

// Exists checks whether a binary is available on PATH.
func (d *Runner) Exists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// RequireTool returns an error if the named binary is not on PATH.
func (d *Runner) RequireTool(name, installHint string) error {
	if d.Exists(name) {
		return nil
	}
	msg := fmt.Sprintf("%s not found on PATH", name)
	if installHint != "" {
		msg = fmt.Sprintf("%s not found on PATH; install: %s", name, installHint)
	}
	return apperr.NotFound(msg)
}

// runWithEnv is the shared implementation for Run and RunWithEnv.
func (d *Runner) runWithEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) error {
	cmd := exec.CommandContext(
		ctx,
		name,
		args...,
	) //nolint:gosec // caller-controlled args
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), extraEnv...)
	configureProcessGroup(cmd)

	if err := cmd.Run(); err != nil {
		cs := name
		if len(args) > 0 {
			cs = name + " " + strings.Join(args, " ")
		}
		if ctx.Err() != nil {
			return apperr.Wrap(ctx.Err(), apperr.CodeInternal, cs+" interrupted")
		}
		return apperr.Wrap(err, apperr.CodeInternal, cs+" failed")
	}
	return nil
}
