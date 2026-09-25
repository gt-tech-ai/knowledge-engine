# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/). Go and Python (`techai-webutils`) ship together from one tag.

## [0.2.0] - 2026-09-25

0.2.0 makes the library product-agnostic: everything one consumer's product needed (its event
catalog, authorization model, config sections, identity provider, key layouts, retrieval scope and
wording) is removed or replaced by a seam the consumer fills. The Go module retracts v0.1.0-rc.1
through v0.1.5 (see [Retracted](#retracted)); the `techai-webutils` 0.1.x releases stay on PyPI and
are not yanked.

### Breaking changes and migration

Everything under [Removed](#removed) is breaking too.

Go:

- Auth interceptor: `NewAuthInterceptor(stub, headers HeaderMap)` and `ServerBuilder.WithAuth(stub,
  headers)` take the gateway header names (no defaults; they replace the `HeaderAuth*` constants)
  and panic when `HeaderMap.Sub` is empty. `AuthClaims.OrgID` is `TenantID`, `InitialRoles` is
  `Roles`, and `Teams`/`Workspaces` are gone. Roles are trimmed of surrounding whitespace, and the
  stub dev claims' tenant is `stub-tenant`, where it was `stub-org`.
- `ServerBuilder.WithIdentity` / `WithTenant` are replaced by `WithInterceptors` with the consumer's
  `NewPrincipalInterceptor` and `NewTenantScopeInterceptor`. Wiring mistakes panic at startup
  rather than failing per request: `NewPrincipalInterceptor(nil, …)`, `NewTenantScopeInterceptor`
  with a nil extractor, no stampers or a nil stamper, `SessionVarStamper("")` and a nil
  `WithInterceptors` entry. `GetAuthClaims` returns `ok=false` for nil claims, which is what the
  principal interceptor leaves behind; read the caller with `core/principal.PrincipalFrom`.
- Events: `EventMetadata.OrgID` (JSON `org_id`) is `TenantID` (JSON `tenant_id`). A payload that
  still carries `org_id` decodes with an empty `TenantID`, so upgrade producers and consumers
  together, or drain or dual-read the events already persisted or in flight (outbox rows, queued
  messages). A consumer that must keep its payload's field names can define its own metadata
  struct with its own JSON tags and convert it to `EventMetadata` in each event's `Metadata()`.
- Config loader: a nil `Schema` derives no bindings (pass `WithSchema`), the default env prefix is
  empty (pass `WithEnvPrefix`), and `ResolveEnv` reads `APP_ENV` then `ENVIRONMENT`. Keys read with
  `GetString` bind through `WithExtraEnv`.
- `transport.IdentityClientConfig` and `transport.Config.IdentityClient` (key
  `transport.identity_client`) are removed; nothing in the library read them. Keep that setting in
  the consumer's own config section.
- `infra.AuthConfig`: the Auth0 block is a generic OIDC block (`auth.oidc.issuer`, `audience`,
  `client_id`; no env aliases); `Validate` requires issuer + audience when `stub` is false.
- `infra.S3Config`: `ImagesBucket` is gone and `Bucket` has no default; `infra.SQSConfig` defaults
  to no queues; `workers.DefaultConfig` sets no service name or port; the consumer-prefixed
  tracing env aliases are gone (`OTEL_EXPORTER_OTLP_ENDPOINT` stays); `stores.Config` drops the
  pending-indexing page sizes.
- S3 connector (security): `iam_role` auth requires an `ExternalID`. Without one, a tenant who
  registers another tenant's role ARN could have the platform assume it for them (the
  confused-deputy problem). Build also refuses (`CodeInvalidInput`) an `ExternalID` outside STS's
  format (2–1224 characters of `[\w+=,.@:/-]`), `iam_role` without a role ARN or STS client, and
  `access_key` without a key id or secret. Migration: give every stored `iam_role` connector an
  ExternalID that your platform generates per tenant and stores with the connector (never one
  taken from tenant input), and have each tenant add a matching `sts:ExternalId` condition to the
  role's trust policy. Until then, those connectors fail at build.
- S3 connector errors: `TestConnection` / `ListPage` report credential failures (e.g. STS
  AccessDenied for a wrong ExternalId) and S3 4xx responses other than 408/429 (AccessDenied,
  InvalidAccessKeyId) as `CodeInvalidInput` instead of `CodeUnavailable`, so callers that retry on
  Unavailable stop retrying them. The credentials provider handed to an `S3ClientBuilder` is now a
  wrapper: `errors.As` still reaches the SDK error, but a type assertion to an SDK provider type
  fails.
- Database errors: Postgres `Ping`, `Check` and `Client.Start`/`Readiness` code an unreachable
  database `CodeUnavailable` (was `CodeInternal`), and so do GORM `Client.Start`/`Readiness` (were
  `CodeInternal` or uncoded). `CodeUnavailable` is transient, so retry policies now retry these.
- gRPC service auth: the service-auth client interceptors (unary and stream) add no bearer when the
  call already carries an `authorization` value, so a caller-set value now replaces the service
  token instead of both being sent.
- Mocks (`go/tests/mocks`): the mocks of removed interfaces are gone (`MockAuthProvider`,
  `MockIdentityResolver`, `MockInviter`, `MockMemberPager`, `MockMemberRoleLister`,
  `MockOrgPager`, `MockOrgProvider`, `MockOrgWriter`, `MockUserProvider`, `MockUserProvisioner`),
  and so are eleven that nothing here used: `MockCacheInvalidator`, `MockCountingPublisher`,
  `MockEventPublisher`, `MockJobEnqueuer`, `MockJobScheduler`, `MockLocker`,
  `MockMessageConsumer`, `MockRepository`, `MockTransaction`, `MockTransactionManager` and
  `MockWorkerRegistry`. Generate your own with mockgen from the `core/interfaces` type.
- Replay buffer: the Redis backend's keys default to `replay:{key}` (and `replay:{key}:lw`), where
  0.1.x always wrote `ws:replay:{key}`. Set `KeyPrefix: "ws:replay:"` on `replaybuffer.Config` (or
  `redis.Config`) to keep the existing layout, or a reconnect straddling the upgrade finds no
  buffered tail.

Python:

- `RetrievalEngine.retrieve(query, *, top_k, filters, index_id)` replaces
  `retrieve(query, workspace_id, top_k, filters, knowledge_base_id)`: the scope rides in `filters`.
- `FilteringRetrievalEngine(inner, policies)` has no default rules and no `min_score` (use
  `MinScore`); `new_retrieval_engine_from_config(config, *, policies, ...)` requires the scope
  policies and applies `MinScore(config.min_score)`. `RetrievalConfig.min_score` defaults to 0.5
  for every kind and must be a number: `None`, which 0.1.x read as "use the kind's default", raises
  `TypeError`. Bedrock scores run lower than vector scores, so set the floor per kind.
- `VectorRetrievalEngine` no longer pushes the scope down to the vector store (0.1.x always sent
  the workspace id). Nothing is pushed unless you name the filter keys: `store_filter_keys` on
  `new_retrieval_engine_from_config`, `filter_keys` on the engine. Without them, `top_k` is taken
  across every tenant and then post-filtered, which silently drops in-scope results; the factory
  logs a warning when a `MetadataEquals` scope is not pushed down.
- Bedrock: no filter is pushed down and no document id is derived from object keys unless the
  consumer injects `filter_builder(filters)` / `document_id_resolver(metadata, filters)`; the
  default reads the chunk's `document_id` metadata, else its `x-amz-bedrock-kb-source-uri`.
  Retrieve failures raise coded `AppError`s (`UNAVAILABLE` or `INTERNAL`) instead of raw botocore
  `ClientError` / `BotoCoreError`. `index_id=""` raises `InternalError` (only `None` selects the
  configured index), so a router that maps tenants to indexes must return `None` or fail for an
  unmapped tenant, not `""`.
- `StubRetrievalEngine` serves the passages it is given (none by default).
- `Citation` drops `trust_level`, `classification`, `s3_key` and `format` (use `attributes`).
- `KnowledgeBase.index_document(document_id, chunks, *, attributes, document_name)`;
  `remove_document`, `get_document_status` and `sync` drop `workspace_id`; `KBDocument` drops it.
  Document ids are now unique across the knowledge base, not per workspace: namespace them yourself
  (e.g. `f"{tenant}:{document_id}"`) or two tenants' documents with the same id collide.
- `VectorKnowledgeBase` raises `ValueError` for the reserved attribute keys (`document_id`,
  `content`, `entry_id`, `document_name`, `chunk_index`) and re-embeds a document whose attributes
  or name changed instead of skipping it on resume. Resume reads the new
  `VectorStore.stored_metadata`, from which `existing_ids` is now derived: a custom `VectorStore`
  overrides `stored_metadata` (not `existing_ids`) to keep resume working.
- `PostgresAdvisoryLock(dsn, key, namespace)` and `LockConfig.key` / `namespace` replace
  `knowledge_base_id` / `data_source_id` (keep the old identity with `key=f"{kb}:{ds}"` and
  `namespace=0x4B425359`). `LockConfig.namespace` has no default; the postgres kind raises
  `ValueError` without one. The lock session's `application_name` comes from
  `LockConfig.application_name` (default `advisory-lock`; 0.1.x hardcoded `app-kb-lock`), so set
  `application_name="app-kb-lock"` to keep `pg_stat_activity` queries that match the old name.
- Settings: the prefixed base settings class is replaced by `BaseAppSettings`, with no env
  prefix; `initialize_config` / `apply_yaml_defaults` default to no prefix and to the
  `APP_ENV`/`ENVIRONMENT` selectors. Set a
  prefix: with none, YAML keys are exported under bare names (`AWS_REGION`, `DEBUG`) that collide
  with ambient variables such as Kubernetes service links (`REDIS_PORT=tcp://…`). A second
  `initialize_config` call with a different `config_dir`, `env_prefix` or `env_selectors` raises
  `ValueError` (0.1.x ignored it); the same arguments, with or without the prefix's trailing `_`,
  are still a no-op.
- gRPC auth: `HeaderClaimMapping(user_id, tenant_id, roles)` has no defaults;
  `AuthServerInterceptor(headers)` and `ServerInterceptorBuilder.with_auth(headers)` require it;
  `AuthClaims.org_id` is `tenant_id` and `clearance_level` is gone.
- gRPC service auth (security): `ServerInterceptorBuilder.with_service_auth` raises `ValueError`
  on an empty token instead of silently accepting every call; pass `allow_unauthenticated=True`
  for local dev. It accepts a comma-separated `current,previous` pair for rotation and compares in
  constant time, so a token that contains a comma is now two accepted tokens.
- LLM: `LlmConfig.model` has no default, and an empty model raises `ValueError` for both the
  `bedrock` and `ollama` kinds; `DEFAULT_MODEL` / `DEFAULT_REWRITE_MODEL` are gone.
- gRPC boundary errors read `gRPC upstream <STATUS>` instead of naming a consumer service.

### Added

- `ARCHITECTURE.md`: the design rules the code follows (layers, swappable components, interface
  composition, dependency injection, decorators and their order, configuration, error codes,
  stub-first backends, what belongs here, execution, testing, releases). Code comments cite its
  sections as `ARCHITECTURE.md#<section>`.
- Config (`go/foundation/config`):
  - `WithSchema`: the consumer's root config struct drives the derived env bindings, following
    mapstructure's tag rules (the name before the comma, `,squash` flattening, the field name for
    an untagged exported field, `-` and `,remain` skipped). A schema that is not a struct or a
    pointer to one fails `Load` with `CodeInvalidInput`.
  - `WithExtraEnv`: bindings for keys outside that struct, or a replacement for a schema key's
    derived binding (its `envalias` names are then not bound). Repeated calls merge, a later entry
    for a key replacing the earlier one.
  - `WithEnvSelectors(selectors...)` and `viper.ResolveEnvFrom(selectors...)`: the consumer picks
    the overlay selector variables.
- Events (`go/core/events`): `Registry` with `Register(type, factory)` and `Parse`, so a consumer
  parses its own event catalog. The zero value is usable; `Register` rejects an empty type, a
  duplicate and a factory that does not return a non-nil pointer; `Parse` rejects a payload with
  no `event_type`.
- Principal (`go/core/principal`): `WithPrincipal[P]` / `PrincipalFrom[P]` carry the request's
  resolved principal (the consumer's own type) in the context, beside `core/tenant`, so every tier
  reads the caller without importing the transport.
- Connect interceptors (`go/clients/transport/connect/interceptors`):
  - `HeaderMap` — the gateway header names the auth interceptor reads.
  - `PrincipalResolver[P]`, `NewPrincipalInterceptor[P]` — resolve claims into the consumer's
    own principal type, stored with `core/principal`. A resolver error maps by code (a
    `*connect.Error` keeps its own, a `core/errors` code maps to the matching Connect code, an
    uncoded error is `Unavailable`); the client sees only `principal resolution failed`, and the
    cause goes to the request log. A nil principal is `Unauthenticated`.
  - `TenantExtractor`, `TenantStamper`, `NewTenantScopeInterceptor`, `StampTenantContext`,
    `SessionVarStamper(name)` — stamp a consumer-resolved tenant with consumer-chosen stampers.
    `uuid.Nil` counts as no tenant, and the stampers are copied at construction.
  - `ServerBuilder.WithInterceptors` — caller-supplied interceptors, run after auth and before
    validation.
- Connector (`go/core/interfaces`): `SourceConfig.ExternalID`, sent as the STS ExternalId on every
  `iam_role` AssumeRole.
- gRPC clients (`go/clients/rpc/grpc/interceptors`): `StreamingClientBuilder.WithServiceAuth` and
  `ServiceAuthStreamClientInterceptor` — a streaming client presents its service-to-service bearer
  token on every RPC of the connection (stream opens and unary calls).
- MinIO test fixture (`go/tests/fixtures/dbtest/minio`): exported `DefaultImage` and a `WithImage`
  option on `NewTestMinIO`, so a consumer can repin the image without an engine release.
- Mocks (`go/tests/mocks`): `MockAssumeRoleAPIClient` and `MockDeduplicator`.
- Replay buffer (`go/clients/replaybuffer`): `Config.KeyPrefix` / `redis.Config.KeyPrefix` and
  `redis.DefaultKeyPrefix`, so buffers can share a Redis or keep an existing key layout.
- Python config: `initialize_config(env_prefix=..., env_selectors=..., strict=...)` and
  `apply_yaml_defaults(config, prefix)`. The prefix may be given with or without its trailing `_`;
  `strict=True` raises on a missing directory, a missing `base.yaml` or bad YAML (by default these
  only warn). The selected overlay is logged, with a warning when a selector names an overlay file
  that does not exist.
- Python retrieval: `PassagePolicy` (a core contract in `core.interfaces.retrieval`, still
  importable from `clients.retrieval.filtering`) with `MinScore`, `MetadataEquals` (admits nothing
  when the request's value is empty) and `OrdinalCeiling` (empty `ranks` raise `ValueError`);
  Bedrock `filter_builder` and `document_id_resolver` seams; `new_retrieval_engine_from_config`
  seams `policies`, `filter_builder`, `document_id_resolver`, `store_filter_keys` and
  `stub_passages`; `StubRetrievalEngine(passages)`; `VectorRetrievalEngine(..., filter_keys=...)`;
  `wrap_retrieval_engine(inner, config, policies=...)`, which gives a consumer's own engine the
  factory's filtering (score floor, then the policies). `clients.retrieval` and
  `clients.retrieval.bedrock` export their public API (including `FilterBuilder`,
  `DocumentIdResolver`, `metadata_document_id` and `SOURCE_URI_KEY`) without importing the AWS or
  Qdrant SDKs.
- Python citations: `Citation.attributes` and `PassageCitationExtractor(attribute_keys=...)`.
- Python gRPC: `HeaderClaimMapping` (header names trimmed and lowercased, a blank name raises
  `ValueError`; exported from `clients.rpc.grpc.interceptors`) and `AuthServerInterceptor(headers)`.
- Python: `LockConfig.application_name`, `VectorStore.stored_metadata`, and
  `foundation.resilience.aws_boundary.botocore_error_to_app_error`, which the Bedrock LLM provider
  and retrieval engine share.

### Security

- Python Bedrock retrieval: a chunk's metadata is carried as-is, so a chunk missing a scope key is
  dropped by a `MetadataEquals` policy instead of inheriting the request's scope.
- The S3 connector's `ExternalID` requirement and Python's empty service token refusal are listed
  under [Breaking changes and migration](#breaking-changes-and-migration).

### Changed

- `config.ViperConfig` is an alias of `viper.Config`, so every loader field (including `Schema`
  and `ExtraEnv`) is settable through `config.NewFromConfig`.
- `.golangci.yml`: the `depguard` layer rules now target this repository's `go/` tree. Previously
  every rule's file glob pointed at a path that does not exist here, so no layer boundary was
  enforced. The `go/helpers` and `go/services` rules also match the files at those directories'
  roots, and `go/helpers` may not import `go/transport`.
- `.gitleaks.toml`: a minimal generic configuration (default rules plus build/venv allowlists).
- Integration tests (Go fixture and Python conftest) run MinIO from `cgr.dev/chainguard/minio`
  pinned by digest; the previous `quay.io/minio/minio` image can no longer be pulled.
- `e2ekit.PollTraceInTempo` returns a `CodeUnavailable` error wrapping the last failure when its
  deadline passes.
- Python retrieval: the filter log line carries a `dropped_by` count per policy type; the factory
  warns when a `MetadataEquals` scope has no push-down for the selected kind and logs unused seams
  at info; the vector engine warns when `index_id` names a collection other than its own.
- Python gRPC: `ServerInterceptorBuilder.build()` warns on the `allow_unauthenticated` bypass and on
  `with_auth` without service auth.
- Python lock: every log line carries the lock's key and namespace.
- The `techai-webutils` PyPI description is now `python/techai_webutils/README.md` (it was empty).
- Comments, docstrings, docs and test fixtures were reworded to drop references to code, documents
  and trackers outside this repository.

### Removed

Go (product code a consumer now owns):

- `core/events`: the domain event catalog and `ParseEvent` (use `Registry`).
- `foundation/storagekey`, `clients/jobs/connectorsync`, `lock.ConversationLockKey`,
  `interfaces.AuthConfig`.
- `clients/auth` (the Auth0 Organizations backend and stub), `interfaces.OrgProvider`,
  `interfaces.UserProvider`, the Auth0 types in `core/types`, the prod guard.
- The `HeaderAuth*` gateway header constants (pass a `HeaderMap`).
- The identity interceptor's authorization model (`UserPermissions`, `WithUserPermissions`,
  `GetUserPermissions`, `RequireUserPermissions`, `TeamMembership`, `WorkspaceAccess`,
  `IdentityResolver`, `NewIdentityInterceptor`, `StubInternalOrgID`, `StubOrgExternalID`),
  `NewTenantInterceptor`, `TenantSessionVar`, `WithTenantFromPerms`.
- The unused `interfaces.AuthProvider`, `AuthIdentity` and `types.TenantContext`.
- The root `schema` package (`AppConfig`, `ServerGroup`, `StorageConfig`, `MessagingConfig`),
  `schema/services`, and the notifications, pending-notifications and crypto sections.
- `tests/fixtures/e2ekit` product effects (telemetry and `PollUntil` stay) and
  `foundation/golang` (repository-layout discovery).

Python:

- `clients/kb_registry` and `core/interfaces/kb_registry`, `core/interfaces/document_status`,
  `core/interfaces/ingestion_job_state`, `pipelines/title` and `TitleGenerator`.
- The unused `core/events`, `core/interfaces/{event_publisher,connection,query,auth}` and
  `TenantContext`.
- `CLEARANCE_FILTER_KEY` and `allowed_classifications_for`.

### Fixed

- Config: under a custom env prefix, the derived bindings no longer also honour another prefix's
  env vars; the configured prefix alone names them.
- Errors: `AppError.StackTrace()` starts at the code that created the error. It matched this
  package's frames by a file path that no longer exists, so every trace began inside `New`/`Wrap`.
- Config: `DatabaseConfig.DSN()` URL-escapes the user and password, so credentials containing
  `@ : / ? # %` or brackets no longer split the connection URL, and it accepts a bracketed IPv6
  host (`[::1]`).
- GORM: `Client.Start` closes its pool when the first ping fails (it leaked), the ping honours
  `ctx`, and `DB()` stays nil after a failed start.
- Memory storage: `CompleteMultipartUpload` with a bucket or key that doesn't match the upload
  returns `CodeNotFound` and writes nothing (it wrote the object under the given key).
- Test fixtures: the Postgres, Redis, MinIO and ElasticMQ fixtures terminate a container that
  failed to start.
- River: the `MaxWorkers` docs say the cap is per client (per replica), not fleet-wide.
- Python event stack: an open circuit breaker no longer creates the handler's coroutine, so no
  "coroutine was never awaited" warning is emitted.
- Python LLM fallback: `FallbackLlmProvider` falls back on a throttled or 5xx primary again when the
  primary raises a coded `AppError` wrapping the botocore error (as the Bedrock provider does); it
  only recognised a raw `ClientError`, so the fallback never fired for the real provider.

### Retracted

- `v0.1.0-rc.1` through `v0.1.5` of the Go module (`go.mod` `retract`). The `techai-webutils`
  0.1.x releases on PyPI are not yanked.
