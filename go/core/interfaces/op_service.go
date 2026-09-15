package interfaces

import "context"

// OpService is the generic operation-service contract implemented by CLI
// services. Unlike Service[T,P,ID] (entity CRUD), an OpService exposes a single
// decoratable entrypoint: a caller runs one of the service's operations by
// passing typed Args and receiving a typed Result. Multi-operation services
// model their operations as a tagged union in Args (an operation discriminator
// plus per-operation fields) and switch on it inside Run.
//
// A is the operation argument type (often a tagged union for multi-op services);
// R is the operation result type.
//
// Result-type contract (tools/cli OpServices, audit G1). A CLI OpService's R is one
// of exactly two archetypes, so the CLI stays uniform enough for a future
// `search gen command` scaffolder:
//
//   - types.StepResults — GATE-PRODUCING: the operation fans out into per-item steps
//     that a gate renders (lint, testrun's suites, build, format, security, …). The
//     error channel signals a hard failure; the steps carry per-item pass/fail/warn.
//   - struct{} — CMD-DIRECT: the operation has no per-item breakdown; success/failure
//     is carried entirely in Run's error (codegen, migrate, check, build's cmd ops).
//
// Any other R is a drift to be migrated to one of these two. The two known holdouts:
//   - testrun.TestrunResult (carries Results + Elapsed) → converge on types.StepResults,
//
// folding Elapsed into a StepResults.Detail / observer emission (Task 5).
//   - synthetic.Result (carries *Manifest) → the ops are cmd-direct (wrapped by
//     adapter.Single) and the seed op consumes the manifest internally, so synthetic
//
// migrates to the struct{} archetype (Task 4).
type OpService[A any, R any] interface {
	// Name returns the service name, used as a label in logs, metrics, and
	// decorator actions.
	Name() string

	// Run executes the operation described by args and returns its result. It is
	// the single decoration chokepoint: cross-cutting concerns (logging, metrics,
	// tracing, recovery) wrap Run rather than each individual operation.
	Run(ctx context.Context, args A) (R, error)
}
