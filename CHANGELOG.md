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

### Changed

- `.golangci.yml`: the `depguard` layer rules now target this repository's `go/` tree. Previously
  every rule's file glob pointed at a path that does not exist here, so no layer boundary was
  enforced.
- `.gitleaks.toml`: a minimal generic configuration (default rules plus build/venv allowlists).
- Comments, docstrings and docs no longer reference anything outside this repository.
