# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/). Go and Python (`techai-webutils`) ship together from one tag.

## [Unreleased] — 0.2.0

0.2.0 makes the library product-agnostic: everything one consumer's product needed (its event
catalog, authorization model, config sections, identity provider, key layouts, retrieval scope and
wording) is removed or replaced by a seam the consumer fills. v0.1.x is retracted.

### Added

- `ARCHITECTURE.md`: the design rules the code follows (layers, swappable components, interface
  composition, dependency injection, decorators and their order, configuration, error codes,
  stub-first backends, what belongs here, execution, testing, releases). Code comments cite its
  sections as `ARCHITECTURE.md#<section>`.
- Config (`go/foundation/config`): `WithSchema` (the consumer's root config struct drives the derived
  env bindings), `WithExtraEnv` (bindings for keys outside that struct) and
  `viper.ResolveEnvFrom(selectors...)` (the consumer picks the overlay selector variables).
- Events (`go/core/events`): `Registry` with `Register(type, factory)` and `Parse`, so a consumer
  parses its own event catalog.
- Connect interceptors (`go/clients/transport/connect/interceptors`):
  - `HeaderMap` — the gateway header names the auth interceptor reads.
  - `PrincipalResolver[P]`, `NewPrincipalInterceptor[P]`, `WithPrincipal[P]`, `PrincipalFrom[P]` —
    resolve claims into the consumer's own principal type.
  - `TenantExtractor`, `TenantStamper`, `NewTenantScopeInterceptor`, `StampTenantContext`,
    `SessionVarStamper(name)` — stamp a consumer-resolved tenant with consumer-chosen stampers.
  - `ServerBuilder.WithInterceptors` — caller-supplied interceptors, run after auth and before
    validation.
- Connector (`go/core/interfaces`): `SourceConfig.ExternalID`, sent as the STS ExternalId on every
  `iam_role` AssumeRole.
- MinIO test fixture (`go/tests/fixtures/dbtest/minio`): exported `DefaultImage` and a `WithImage`
  option on `NewTestMinIO`, so a consumer can repin the image without an engine release.
- Python config: `initialize_config(env_prefix=..., env_selectors=...)` and
  `apply_yaml_defaults(config, prefix)`.
- Python retrieval: `PassagePolicy` with `MinScore`, `MetadataEquals` and `OrdinalCeiling`; Bedrock
  `filter_builder` and `document_id_resolver` seams; `new_retrieval_engine_from_config` seams
  `policies`, `filter_builder`, `document_id_resolver`, `store_filter_keys` and `stub_passages`;
  `StubRetrievalEngine(passages)`; `VectorRetrievalEngine(..., filter_keys=...)`.
- Python citations: `Citation.attributes` and `PassageCitationExtractor(attribute_keys=...)`.
- Python gRPC: `HeaderClaimMapping` and `AuthServerInterceptor(headers)`.

### Security

- **Breaking:** the S3 connector refuses `iam_role` auth without an `ExternalID`
  (`CodeInvalidInput`). Without one, a tenant who registers another tenant's role ARN could have
  the platform assume it for them (the confused-deputy problem).
- **Breaking:** Python `ServerInterceptorBuilder.with_service_auth` raises `ValueError` on an empty
  token instead of silently accepting every call; pass `allow_unauthenticated=True` for local dev.
- Python Bedrock retrieval: a chunk's metadata is carried as-is, so a chunk missing a scope key is
  dropped by a `MetadataEquals` policy instead of inheriting the request's scope.

### Changed (breaking)

Go:

- Auth interceptor: `NewAuthInterceptor(stub, headers HeaderMap)` and `ServerBuilder.WithAuth(stub,
  headers)` take the gateway header names (no defaults). `AuthClaims.OrgID` is `TenantID`,
  `InitialRoles` is `Roles`, and `Teams`/`Workspaces` are gone.
- `ServerBuilder.WithIdentity` / `WithTenant` are replaced by `WithInterceptors` with the consumer's
  `NewPrincipalInterceptor` and `NewTenantScopeInterceptor`.
- Config loader: a nil `Schema` derives no bindings (pass `WithSchema`), the default env prefix is
  empty (pass `WithEnvPrefix`), and `ResolveEnv` reads `APP_ENV` then `ENVIRONMENT`. Keys read with
  `GetString` bind through `WithExtraEnv`.
- `infra.AuthConfig`: the Auth0 block is a generic OIDC block (`auth.oidc.issuer`, `audience`,
  `client_id`; no env aliases); `Validate` requires issuer + audience when `stub` is false.
- `infra.S3Config`: `ImagesBucket` is gone and `Bucket` has no default; `infra.SQSConfig` defaults
  to no queues; `workers.DefaultConfig` sets no service name or port; the tracing
  `SEARCH_TRACING_*` aliases are gone (`OTEL_EXPORTER_OTLP_ENDPOINT` stays); `stores.Config` drops
  the pending-indexing page sizes.

Python:

- `RetrievalEngine.retrieve(query, *, top_k, filters, index_id)` replaces
  `retrieve(query, workspace_id, top_k, filters, knowledge_base_id)`: the scope rides in `filters`.
- `FilteringRetrievalEngine(inner, policies)` has no default rules and no `min_score` (use
  `MinScore`); `new_retrieval_engine_from_config(config, *, policies, ...)` requires the scope
  policies and applies `MinScore(config.min_score)`; `RetrievalConfig.min_score` defaults to 0.5
  for every kind.
- Bedrock: no filter is pushed down and no document id is derived from object keys unless the
  consumer injects `filter_builder(filters)` / `document_id_resolver(metadata, filters)`; the
  default reads the chunk's `document_id` metadata.
- `StubRetrievalEngine` serves the passages it is given (none by default).
- `Citation` drops `trust_level`, `classification`, `s3_key` and `format` (use `attributes`).
- `KnowledgeBase.index_document(document_id, chunks, *, attributes, document_name)`;
  `remove_document`, `get_document_status` and `sync` drop `workspace_id`; `KBDocument` drops it.
- `PostgresAdvisoryLock(dsn, key, namespace)` and `LockConfig.key` / `namespace` replace
  `knowledge_base_id` / `data_source_id` (keep the old identity with `key=f"{kb}:{ds}"` and
  `namespace=0x4B425359`).
- Settings: `BaseSearchSettings` is `BaseAppSettings` with no env prefix; `initialize_config` /
  `apply_yaml_defaults` default to no prefix and to the `APP_ENV`/`ENVIRONMENT` selectors.
- gRPC auth: `HeaderClaimMapping(user_id, tenant_id, roles)` has no defaults;
  `AuthServerInterceptor(headers)` and `ServerInterceptorBuilder.with_auth(headers)` require it;
  `AuthClaims.org_id` is `tenant_id` and `clearance_level` is gone.
- LLM: `LlmConfig.model` has no default and `DEFAULT_MODEL` / `DEFAULT_REWRITE_MODEL` are gone.
- gRPC boundary errors read `gRPC upstream <STATUS>` instead of `api InternalService <STATUS>`.

### Removed

Go (product code a consumer now owns):

- `core/events`: the domain event catalog and `ParseEvent` (use `Registry`).
- `foundation/storagekey`, `clients/jobs/connectorsync`, `lock.ConversationLockKey`,
  `interfaces.AuthConfig`.
- `clients/auth` (the Auth0 Organizations backend and stub), `interfaces.OrgProvider`,
  `interfaces.UserProvider`, the Auth0 types in `core/types`, the prod guard.
- The identity interceptor's authorization model (`UserPermissions`, team/workspace access,
  `IdentityResolver`, `NewIdentityInterceptor`, `StubInternalOrgID`), `NewTenantInterceptor`,
  `TenantSessionVar`, `WithTenantFromPerms`.
- The unused `interfaces.AuthProvider`, `AuthIdentity` and `types.TenantContext`.
- The root `schema.AppConfig`/`ServerGroup`, `schema/services`, and the notifications,
  pending-notifications and crypto sections.
- `tests/fixtures/e2ekit` product effects (telemetry and `PollUntil` stay) and
  `foundation/golang` (repository-layout discovery).

Python:

- `clients/kb_registry` and `core/interfaces/kb_registry`, `core/interfaces/document_status`,
  `core/interfaces/ingestion_job_state`, `pipelines/title` and `TitleGenerator`.
- The unused `core/events`, `core/interfaces/{event_publisher,connection,query,auth}` and
  `TenantContext`.
- `CLEARANCE_FILTER_KEY`, `allowed_classifications_for`, `default_policies`,
  `workspace_clearance_filter` and `document_id_from_key`.

### Changed

- `.golangci.yml`: the `depguard` layer rules now target this repository's `go/` tree. Previously
  every rule's file glob pointed at a path that does not exist here, so no layer boundary was
  enforced.
- `.gitleaks.toml`: a minimal generic configuration (default rules plus build/venv allowlists).
- Integration tests (Go fixture and Python conftest) run MinIO from `cgr.dev/chainguard/minio`
  pinned by digest; the previous `quay.io/minio/minio` image can no longer be pulled.
- Comments, docstrings, docs and test fixtures no longer reference anything outside this
  repository.

### Fixed

- Config: under a custom env prefix, the derived bindings no longer also honour another prefix's
  env vars; the configured prefix alone names them.
- Errors: `AppError.StackTrace()` starts at the code that created the error. It matched this
  package's frames by a file path that no longer exists, so every trace began inside `New`/`Wrap`.
- Config: `DatabaseConfig.DSN()` URL-escapes the user and password, so credentials containing
  `@ : / ? # %` or brackets no longer split the connection URL.

### Retracted

- `v0.1.0-rc.1` through `v0.1.5` (`go.mod` `retract`).
