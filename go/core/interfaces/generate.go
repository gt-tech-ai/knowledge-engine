package interfaces

// Mock generation directives for core interfaces.
// Run: go generate ./go/core/interfaces
// All generated mocks are written to go/tests/mocks/.

// Storage
//go:generate mockgen -destination=../../tests/mocks/mock_storage.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces StorageClient

// Messaging
//go:generate mockgen -destination=../../tests/mocks/mock_messaging.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces MessagePublisher,CountingPublisher,MessageConsumer,DynamicConsumer,PatternConsumer

// Connector (integration family) — the AWS-free source read seam + its builder
//go:generate mockgen -destination=../../tests/mocks/mock_connector.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces ConnectorSource,SourceBuilder

// Cache invalidation (non-generic)
//go:generate mockgen -destination=../../tests/mocks/mock_cache_invalidator.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces CacheInvalidator

// Transaction management
//go:generate mockgen -destination=../../tests/mocks/mock_transaction.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces Transaction,TransactionManager

// Jobs
//go:generate mockgen -destination=../../tests/mocks/mock_jobs.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces Job,Worker,JobEnqueuer,JobScheduler,WorkerRegistry

// Execution job (AnyJob — the execution fan-out job seam decorated by job/decorators)
//go:generate mockgen -destination=../../tests/mocks/mock_exec_job.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces AnyJob

// Events
//go:generate mockgen -destination=../../tests/mocks/mock_events.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces EventPublisher

// ByteCache (non-generic cache)
//go:generate mockgen -destination=../../tests/mocks/mock_cache.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces ByteCache

// Logger
//go:generate mockgen -destination=../../tests/mocks/mock_logger.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces Logger

// Metrics
//go:generate mockgen -destination=../../tests/mocks/mock_metrics.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces Metrics,Counter,Histogram,Gauge

// Tracer
//go:generate mockgen -destination=../../tests/mocks/mock_tracer.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces Tracer,Span

// ConfigLoader
//go:generate mockgen -destination=../../tests/mocks/mock_config_loader.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces ConfigLoader

// Retrier
//go:generate mockgen -destination=../../tests/mocks/mock_retrier.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces Retrier

// CircuitBreaker
//go:generate mockgen -destination=../../tests/mocks/mock_circuit_breaker.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces CircuitBreaker

// LeaderElector (single-instance execution gate — drives the job/decorators leader gating)
//go:generate mockgen -destination=../../tests/mocks/mock_leader.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces LeaderElector

// DistributedLock (keyed, token-fenced cross-pod lock — the primitive backend seam) +
// Locker (DistributedLock + Hold — the consumer-facing lock seam the query guard consumes)
//go:generate mockgen -destination=../../tests/mocks/mock_lock.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces DistributedLock,Locker

//go:generate mockgen -destination=../../tests/mocks/mock_replaybuffer.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces ReplayBuffer

// Bulkhead
//go:generate mockgen -destination=../../tests/mocks/mock_bulkhead.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces Bulkhead

// RateLimiter
//go:generate mockgen -destination=../../tests/mocks/mock_rate_limiter.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces RateLimiter

// Store
//go:generate mockgen -destination=../../tests/mocks/mock_store.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces Store

// Repository
//go:generate mockgen -destination=../../tests/mocks/mock_repository.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces Repository

// Service
//go:generate mockgen -destination=../../tests/mocks/mock_service.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces Service

// Pipeline
//go:generate mockgen -destination=../../tests/mocks/mock_pipeline.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces Pipeline

// Workflow
//go:generate mockgen -destination=../../tests/mocks/mock_workflow.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces Workflow

// CommandRunner + CmdHook (external-command execution seam + its lifecycle hook)
//go:generate mockgen -destination=../../tests/mocks/mock_command_runner.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces CommandRunner,CmdHook

// Runnable (runner-less execution unit) + ToolSpec (a job's required external tool)
//go:generate mockgen -destination=../../tests/mocks/mock_execution.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces Runnable,ToolSpec

// ExecutionObserver (gate/engine lifecycle callbacks — the render/progress seam)
//go:generate mockgen -destination=../../tests/mocks/mock_observer.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces ExecutionObserver

// DeadLetterBackend (dead-letter sink — the swallow-and-report DLQ facade seam)
//go:generate mockgen -destination=../../tests/mocks/mock_deadletter.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces DeadLetterBackend

// Lifecycle (coordinated startup/shutdown seam managed by lifecycle.Manager)
//go:generate mockgen -destination=../../tests/mocks/mock_lifecycle.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/interfaces Lifecycle

// grpc.ClientStream (external seam — stream client interceptor tests drive it
// without a network connection; the fixtures.StubClientStream factory configures it)
//go:generate mockgen -destination=../../tests/mocks/mock_client_stream.go -package=mocks google.golang.org/grpc ClientStream
