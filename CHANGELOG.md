# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[SemVer](https://semver.org/). Go and Python (`techai-webutils`) ship together from one tag.

## [Unreleased] — 0.2.0

### Added

- `ARCHITECTURE.md`: the design rules the code follows (layers, swappable components, interface
  composition, dependency injection, decorators and their order, configuration, error codes,
  stub-first backends, what belongs here, execution, testing, releases). Code comments cite its
  sections as `ARCHITECTURE.md#<section>`.
- Config (`go/foundation/config`): `WithSchema` / `viper.Config.Schema` (the consumer's root config
  struct drives the derived env bindings), `WithExtraEnv` / `viper.Config.ExtraEnv` (env bindings
  for keys outside that struct), and `viper.ResolveEnvFrom(selectors...)` (the consumer picks the
  env vars that select the overlay).
- Events (`go/core/events`): `Registry` with `Register(type, factory)` and `Parse`, so a consumer
  parses its own event catalog.
- Connect interceptors (`go/clients/transport/connect/interceptors`):
  - `HeaderMap`, `DefaultHeaderMap`, `NewAuthInterceptorWithHeaders` and
    `ServerBuilder.WithAuthHeaders` — read claims from the headers a gateway actually sets.
  - `PrincipalResolver[P]`, `NewPrincipalInterceptor[P]`, `WithPrincipal[P]`, `PrincipalFrom[P]` —
    resolve claims into the consumer's own principal type.
  - `TenantExtractor`, `TenantStamper`, `NewTenantScopeInterceptor`, `StampTenantContext`,
    `SessionVarStamper(name)` — stamp a consumer-resolved tenant with consumer-chosen stampers.
  - `ServerBuilder.WithInterceptors` — caller-supplied interceptors run after auth, identity and
    tenant, before validation.
- Python config (`techai_webutils.foundation.config`): `initialize_config(env_prefix=...,
  env_selectors=...)` exports the YAML under the consumer's prefix and selects the overlay from the
  consumer's env vars; `apply_yaml_defaults(config, prefix)`.
- Python retrieval (`techai_webutils.clients.retrieval`):
  - `PassagePolicy` with `MinScore`, `MetadataEquals`, `OrdinalCeiling` and `default_policies`;
    `FilteringRetrievalEngine(..., policies=...)`.
  - Bedrock: `filter_builder` and `document_id_resolver` (defaults `workspace_clearance_filter`,
    `document_id_from_key`).
  - `new_retrieval_engine_from_config(config, *, policies, filter_builder, document_id_resolver)`.
- Python citations: `Citation.attributes` and `PassageCitationExtractor(attribute_keys=...)`.
- Python gRPC: `HeaderClaimMapping`, `AuthServerInterceptor(headers=...)` and
  `ServerInterceptorBuilder.with_auth(headers=...)`.

### Changed

- `.golangci.yml`: the `depguard` layer rules now target this repository's `go/` tree. Previously
  every rule's file glob pointed at a path that does not exist here, so no layer boundary was
  enforced.
- `.gitleaks.toml`: a minimal generic configuration (default rules plus build/venv allowlists).
- Comments, docstrings and docs no longer reference anything outside this repository.

### Fixed

- Config: under a custom env prefix, the derived bindings no longer also honour `SEARCH_<PATH>`
  env vars; the configured prefix alone names them.
- Errors: `AppError.StackTrace()` starts at the code that created the error. It matched this
  package's frames by a file path that no longer exists, so every trace began inside `New`/`Wrap`.
- Python Bedrock retrieval: a passage's document id is the key segment after the workspace segment
  wherever it sits, so prefixed key layouts (`{prefix}/{workspace}/{document}/{file}`) no longer
  return the prefix as the document id; a key without the workspace yields no id instead of a guess.
- Python gRPC auth: the claim docstrings name the metadata keys actually read (`x-user-id`, …).
