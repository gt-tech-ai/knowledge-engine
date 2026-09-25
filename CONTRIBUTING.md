# Contributing

Thanks for your interest in the Tech AI Knowledge Engine.

## Ground rules

- **Layered architecture.** Code imports downward only
  (`core → foundation → clients → repos → services → pipelines → workflows → transport`);
  no lateral imports between same-layer packages. `depguard` enforces the layer order; review
  catches lateral imports, which no lint rule checks.
- **Everything swappable is behind an interface, selected by configuration.** A new
  backend is a `Kind` + `Config` + `*_from_config` factory, chosen by a config value
  and injected at a composition root — not a compile-time import.
- **Tests.** Black-box, in the `go/tests/` and `python/techai_webutils/tests/` trees,
  asserting observable behaviour. Unit tests mock the external boundary (mockgen-generated
  mocks in Go, `unittest.mock` in Python); integration tests run the real dependency in a
  Docker container via testcontainers. No hand-rolled fakes.
- **Self-contained.** This repository never references anything outside itself: no
  consumer names, paths or design documents, and no issue/ADR/story ids in code, comments,
  docstrings, tests or config. Consumers may reference into it. Design rules live in
  [ARCHITECTURE.md](ARCHITECTURE.md); cite its sections (`ARCHITECTURE.md#<section>`).
- **No product code.** Product logic stays with its consumer; a consumer plugs its policy into
  a seam here (see [ARCHITECTURE.md](ARCHITECTURE.md#what-belongs-here)).

## Workflow

1. Fork and branch (`<type>/<short-description>`).
2. Make the change with a failing test first where behaviour changes.
3. Regenerate mocks, then run the gates:
   `go generate ./go/tests/mocks/... && go vet ./go/... && golangci-lint run ./go/... && go test ./go/...`
   (Go) and
   `cd python/techai_webutils && uv run ruff check . && uv run basedpyright src && uv run pytest`
   (Python).
4. Open a PR with a conventional-commit title (`feat:`, `fix:`, `refactor:`, …).

## Releases

Versioning follows the git tag as the single source of truth (SemVer). Maintainers
tag `vX.Y.Z`; the release workflow builds the GitHub release and publishes
`techai-webutils` to PyPI when its version is new. Record user-visible changes in
[CHANGELOG.md](CHANGELOG.md).
