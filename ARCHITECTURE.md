# Architecture

The rules every package here follows, cited from code comments as `ARCHITECTURE.md#<section>`.
Go paths are relative to the repo root; Python paths to `python/techai_webutils/src/techai_webutils/`.

## The one idea

Everything is an implementation detail behind an interface. The contract lives in `core`, which
imports no other layer; the implementation is **selected by configuration** (a `Kind`),
**injected explicitly** through constructors, and **wrapped by decorators** for cross-cutting
concerns. Swapping S3 for memory, zap for slog, or asyncio for Ray is a config change.

## Layers and import direction

A Go package imports only its own layer or lower ones: `go/core` (interfaces, types, errors,
events) → `go/foundation` (config, logging, telemetry, resilience,
decorator mechanisms, lifecycle) → `go/clients` (external systems) → `go/repos` → `go/services` →
`go/pipelines` → `go/workflows` → `go/transport` (REST/RPC edges).

Outside the ladder: `go/execution` (batch fan-out; imports only `core`), `go/helpers` (leaf
utilities), and `go/tests` (may import anything). No package imports a higher layer. Integration
families in a tier don't import each other (`clients/connector` is handed a storage builder rather
than importing `clients/storage`); the shared clients-tier pieces are `clients/decorators` and
`clients/interceptorcore`. `.golangci.yml` carries `depguard` rules for these boundaries.

Python mirrors the tiers: `core` → `foundation` (+ `execution`) → `clients` → `repos` →
`services` → `pipelines` → `workflows` → `controllers` (the transport edge). Imports point
downward, except one type-checking-only import in `foundation/resilience/sqs_dlq.py`.

## Swappable components

A swappable component is a **`Kind`** (Go `type Kind int` + `iota` + `String()`; Python `StrEnum`),
a **`Config`** carrying the kind and backend settings, and **a factory** that fails loudly on an
unknown kind:

- Go: `New(kind, opts...)` / `NewFromConfig(cfg)` (`foundation/logger`, `foundation/metrics`, …)
  or `NewFromConfig(ctx, kind, cfg)` at a client tier root (`clients/storage`,
  `clients/messaging`); an unknown kind is a `CodeInvalidInput` error.
- Python: `new_<type>_from_config(config)` (or `<type>_from_config`, e.g. `executor_from_config`);
  an unknown kind raises `ValueError`.

One backend per sub-package beside the factory: `clients/storage/{s3,memory}`,
`clients/lock/{local,redis}`, `foundation/logger/{zap,stdlib}`, `clients/llm/{bedrock,ollama,stub}`.
Python imports heavy or optional dependencies lazily in the selected branch, so the `ray`,
`postgres`, `parsing` and `langdetect` extras load only when chosen (`clients/lock/builder.py`).

## Interface composition

`go/core/interfaces` holds small role interfaces (`Reader`, `Writer`, `Lister`, `Exister`,
`Lifecycle`, `HealthChecker`, …) that larger contracts (`Repository`, `Service`, `Client`) compose.
An interface declared **outside** `core` embeds a core interface (`repos/repository.ReadListExistStore`
embeds `Reader`/`Lister`/`Exister`; `clients/rpc.RPCClient` embeds `interfaces.Client`). Opt-in
capabilities compose rather than widen (`DynamicConsumer` embeds `MessageConsumer`); Python
Protocols do the same (`StorageClient` composes `ManagedResource`).

An out-of-core interface that cannot embed one carries a doc-comment tag saying why:

- `// SDK seam` — mirrors a third-party SDK surface (AWS S3/SQS/Secrets Manager, go-auth0,
  `grpc.ClientConnInterface`) so the adapter can be driven by a generated mock.
- `// sealed union` — a closed implementer set standing in for a sum type (`core/types.Filter`).
- `// interface-composition exemption — <reason>` — a mechanism primitive or narrow consumer-side
  port, not a data-access surface (`foundation/decorator.Executor`, `execution/engine.Step`, …).

Every interface under `go/` outside `core` (tests and mocks aside) embeds a core interface or
carries a tag. Keep the tags verbatim.

## Dependency injection

Collaborators arrive as constructor arguments, `With*` builder calls, or functional options
(`foundation/options.Option[T]`); the Go tree has no `init()` functions or service locators (the
OTel tracer backend is the one piece that installs process-global state). KE is a library: the
consumer's `main` is the single composition root that builds config, calls the factories, applies
decorators, and registers clients with `foundation/lifecycle.Manager` (start in order, stop in
reverse). Lifecycle-managed clients connect in `Start`, not their constructors (`repos/adapters/gorm`,
`clients/database/postgres`). Python backends are `ManagedResource` async context managers; the
documented exception is logging, which structlog configures once and seams resolve by name.

## Decorators

Cross-cutting concerns wrap business logic and backends, never live inside them. A nil
collaborator or zero timeout skips its layer (the service builder always adds recovery).

- **Go — fluent builders** (`NewBuilder(base, name).WithLogging(l).WithTracing(t).Build()`)
  returning the wrapped interface. Shared bodies: `foundation/decorator` (Execute-shaped tiers),
  `foundation/decorate` (`OpMiddleware`, `Chain`, `Exec[R]` for custom repository/service ops), and
  `clients/decorators` (the client-boundary `Stack`: `Run`, `RunStream`, `StackFromConfig`).
  `foundation/decorator` (Go and Python) exposes `Unwrap` so tests can reach the wrapped unit.
- **Python — `__getattr__` proxies** in `clients/decorators/proxy.py` (`LoggingProxy`,
  `RetryProxy`, `CircuitBreakerProxy`, …) apply their concern around each awaited call;
  `new_client_stack_from_config` composes them. Tier builders reuse `foundation/decorator.py`.

Inner logging layers (client stack, repository, service, pipeline, workflow) log failures at Debug
so the outermost recovery/transport seam logs the Error once; the lock and replay-buffer loggers
still log at Error.

### Decorator order

Outermost → innermost:

| Stack | Where | Order |
|---|---|---|
| Client boundary | `go/clients/decorators` | Bulkhead → Retry → CircuitBreaker → Timeout → Tracing → Metrics → Logging |
| Job | `go/clients/jobs/decorators` | LeaderElection → RateLimit → Retry → Timeout → Tracing → Metrics → Logging |
| EventHandler | `go/clients/messaging/decorators` | Dedup → DeadLetter → Retry → CircuitBreaker → Timeout → Tracing → Metrics → Logging |
| Lock | `go/clients/lock/decorators` | Tracing → Metrics → Logging → Timeout → CircuitBreaker → Retry |
| Replay buffer | `go/clients/replaybuffer/decorators` | Tracing → Metrics → Logging → Timeout |
| Cache | `go/clients/cache/decorators` | Metrics → Timeout → CircuitBreaker |
| Connect server | `go/clients/transport/connect/interceptors` | Recovery → RetryBudget → RateLimit → Bulkhead → Metrics → Tracing → Logging → ServiceAuth → Auth → Identity → Tenant → Validate |
| Connect/gRPC client | + `go/clients/rpc/grpc/interceptors` | Metrics → CircuitBreaker → Retry → Timeout → Tracing → Logging (gRPC appends ServiceAuth) |
| gRPC server | `go/clients/rpc/grpc/interceptors` | Recovery → RateLimit → Bulkhead → Metrics → Tracing → Logging |
| Repository | `go/repos/repository/decorators` | Tracing → Metrics → Logging → Timeout → CircuitBreaker → Retry → Caching |
| Service | `go/services/service/decorators` | Recovery → Validation → Tracing → Metrics → Logging → Authorization → Timeout |
| Pipeline, Workflow | `go/{pipelines/pipeline,workflows/workflow}/decorators` | Recovery → Logging → Tracing → Metrics → Timeout |
| Transport handler | `go/transport/decorators` | Recovery → Logging → Metrics → RateLimit → Timeout |
| Execution job | `go/execution/job/decorators` | Tracing → Metrics → Logging → Recovery |
| Python client | `clients/decorators/proxy.py` | Bulkhead → Retry → CircuitBreaker → Timeout → Tracing → Logging |
| Python EventHandler | `clients/decorators/event_stack.py` | Dedup → DeadLetter → Retry → CircuitBreaker → Timeout |
| Python Job | `clients/decorators/job_stack.py` | LeaderElection → RateLimit → Retry → Timeout |

In the client stack Retry applies only to `Retryable` ops (`RunStream` never retries) and sits
outside the breaker, so each attempt is observed. Custom repository ops (`OpChain`) use the
Repository order minus Caching. Tests pin the orders (`go/tests/unit/clients_wrap_order_test.go`,
`services_wrap_order_test.go`, `clients_lock_decorators_test.go`, `foundation_decorator_test.go`).

## Configuration

Config selects; the composition root injects. Go `foundation/config` (Viper) layers `base.yaml` →
`{env}.yaml` → `secrets.yaml` → env vars into typed structs (`foundation/config/schema/*`, each with
`Default…Config()` and a coded-error `Validate()`); `viper.ResolveEnv` picks the overlay. Python
`foundation/config` loads the same hierarchy into pydantic-settings classes.

Business logic never reads the environment. In `go/`, env reads are confined to the config loader,
the `env` secrets backend, subprocess plumbing in `foundation/system`, `helpers.OrDefault`, and one
documented exception: the logger's deploy-time `GIT_SHA` (mirrored in Python).

## Error codes

Every error crossing a package boundary carries an `ErrorCode`. Go `go/core/errors` defines the
codes (`NOT_FOUND`, `INVALID_INPUT`, `UNAUTHORIZED`, `FORBIDDEN`, `CONFLICT`, `TIMEOUT`,
`UNAVAILABLE`, `UPSTREAM`, `INTERNAL`, …); `New(code, msg)` / `Wrap(err, code, msg)` build an
`*AppError` keeping cause and origin stack; `Code(err)` / `Is(err, code)` read it. Production code
under `go/` has no `fmt.Errorf`; `.golangci.yml` bans it via `forbidigo`.

Classification is derived from the code, never the message: `IsTransient` / `IsPermanent`, the
retry policy (`foundation/resilience/retry/exponential.IsRetryable`), the circuit breaker (permanent
domain errors count as successes), and the edges (`ToHTTPStatus`; `transport/rpc.ToConnectError`,
which maps codes and captures the real cause for the request log). Python raises
`AppError(code, message, cause=…)` or a subclass from `core/errors`; `is_transient`, `grpc_status`
and `http_status` derive from the code, retries retry only transient `AppError`s, and boundary
helpers translate SDK errors (`foundation/resilience/grpc_boundary.py`).

## Stub-first backends

Most tiers ship an in-process, stub, or no-op backend, so wiring needs no cloud dependency:

- **Go:** auth `stub`; lock `local`; messaging, replay buffer, storage `memory`; secrets
  `env`/`file`; tracer `noop`; metrics no-op when disabled; logger `stdlib`. Jobs' `NewFromConfig`
  currently returns a no-op River enqueuer.
- **Python:** cache `local`/`null`; email `noop`; embedding, llm, retrieval, kb_ingestion, vector
  `stub`; jobs, lock, messaging, storage `memory`; kb_registry `shared`; tracer, metrics `null`;
  executor `asyncio`.

Go cache (Redis), database (Postgres) and connector (S3) have no stub and are covered by
testcontainers integration tests. `tests/unit/clients_all_stubs_no_network_test.py` builds each
Python stub kind with every real backend constructor patched to fail.

## What belongs here

Generic substrate only: contracts, mechanisms, and adapters any service stack can reuse, including
domain-adjacent building blocks (knowledge-base, retrieval, embedding and LLM clients; RAG pipeline
stages whose prompts and policies the consumer injects). A consumer plugs its own policy into
seams: its event types into an event registry, its principal type into the identity resolver,
its config sections and env conventions into the config loader.

Product code stays with its consumers: domain event catalogs, authorization models, business
rules, prompts, schemas, storage-key layouts, deployables and deployment config.

The repository is self-contained. Consumers may reference it; it never references them — no
consumer names, paths, design documents or tracker ids in code, comments, tests or config.

## Execution substrate

Go `go/execution` models batch work: discover → map → fan out → aggregate.

- `engine`: `FanOut[T]` runs over items with bounded concurrency (a slot is acquired before each
  goroutine spawns), recovers panics into `Fail` results, never lets a unit cancel its siblings;
  `RunJobGroup` runs `AnyJob`s concurrently, streaming `StepResult`s to an `ExecutionObserver`.
- `job`: `job.New[T](name, group)` builds a `Job[T]` (a `Discoverer[T]` plus a mapper to
  `WorkUnit`) with concurrency, retry, and warn-only options; `job/decorators` wraps any `AnyJob`.
- `gate`: `gate.NewGate(name).Parallel(…).Serial(…).StopOnFailure().Build()`; `RunGate` runs phases
  in order and aggregates failures into a `CodeQualityFailed` error.

Python `execution` mirrors these (`fan_out`, `run_job_group`, `new_gate`, `run_gate`) and adds the
`Executor` seam chosen by `executor_from_config`: `AsyncioExecutor` (default), `RayExecutor` over
the `RayRuntime` seam (the lazily imported `RealRayRuntime`, wrapped by the `ResilientRayRuntime` retry decorator), and `PooledExecutor` (a fixed pool of build-once workers).

## Testing

- Go tests are black-box under `go/tests`: `unit/` (external `_test` packages), `integration/`
  (`//go:build integration`, real dependencies via testcontainers), `fixtures/` (container fixtures
  in `fixtures/dbtest/*`, suites, an E2E telemetry kit), and `mocks/`. The one white-box exception
  is `go/clients/jobs/river/runtime_internal_test.go`.
- Unit tests mock only the external boundary, with mocks **generated** by mockgen from
  `go/tests/mocks/generate.go` (`go generate ./go/tests/mocks/...`).
- No hand-rolled fakes replace a dependency an integration test exercises; stubs are shipped
  code, not test doubles. Python unit tests use `unittest.mock`, and `tests/integration`
  (`@pytest.mark.integration`) runs against testcontainers.
- CI (`.github/workflows/ci.yml`): vet, golangci-lint, `go test -race`; ruff, basedpyright, pytest
  (90% unit coverage gate); an integration job for both languages.

## Versioning and releases

The git tag (SemVer `vX.Y.Z`) is the version of record; Go consumers pin the module by tag.
Pushing a `v*` tag runs `.github/workflows/release.yml`, which creates the GitHub release (generated
notes; prerelease for `rc`/`alpha`/`beta` tags) and publishes `techai-webutils` to PyPI only if the
version in `python/techai_webutils/pyproject.toml` (`uv version --short`) is not already there — a
Go-only release is a no-op, not a failure. AutoGitSemVer stamps that version when the package
changes.
