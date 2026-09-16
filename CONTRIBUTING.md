# Contributing

Thanks for your interest in the Tech AI Knowledge Engine.

## Ground rules

- **Layered architecture.** Code imports downward only
  (`core → foundation → clients → repos → services → pipelines → workflows → transport`);
  no lateral imports between same-layer packages. `depguard` enforces this.
- **Everything swappable is behind an interface, selected by configuration.** A new
  backend is a `Kind` + `Config` + `*_from_config` factory, chosen by a config value
  and injected at a composition root — not a compile-time import.
- **Tests.** Black-box, in the `go/tests/` tree, asserting observable behaviour. Unit
  tests mock the external boundary (generated mocks); integration tests run the real
  dependency in a Docker container via testcontainers. No hand-rolled fakes.
- **No tracker ids in source.** Issue/ADR/story ids do not belong in code comments or
  docstrings (the `check comment-refs` gate is blocking).

## Workflow

1. Fork and branch (`<type>/<short-description>`).
2. Make the change with a failing test first where behaviour changes.
3. Regenerate mocks, then run the gates:
   `go generate ./go/tests/mocks/... && go vet ./go/... && golangci-lint run ./go/... && go test ./go/...`
   (Go) and `cd python/techai_webutils && uv run ruff check . && uv run basedpyright src && uv run pytest` (Python).
4. Open a PR with a conventional-commit title (`feat:`, `fix:`, `refactor:`, …).

## Releases

Versioning follows the git tag as the single source of truth (SemVer). Maintainers
tag `vX.Y.Z`; the release workflow builds the GitHub release and publishes the
`techai_webutils` Python package.
