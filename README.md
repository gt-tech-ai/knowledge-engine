# Tech AI Knowledge Engine

A generic, layered service substrate for building multi-language (Go + Python)
backend systems — the reusable "how we build services" library. It carries no
product-domain logic: just the
foundation, clients, repositories, services, pipelines, workflows, transport, and
batch-execution machinery that a service stack is assembled from, behind
dependency-free `core` interfaces.

MIT-licensed and semantically versioned (the git tag is the source of truth).

## Layout

```
go/                     ONE Go module: github.com/gt-tech-ai/knowledge-engine
  core/                 Layer 0 — pure interfaces + types (no external deps)
  foundation/           Layer 1 — config, logger, metrics, tracer, resilience, cache
  clients/              Layer 2 — external-system integration (db, cache, messaging, rpc, storage, lock)
  repos/                Layer 3 — data-access base (repository, cursor, decorators)
  services/             Layer 4 — domain-service base
  pipelines/            Layer 5 — stateless transforms
  workflows/            Layer 6 — multi-step orchestration
  transport/            Layer 7 — HTTP/RPC/JSON:API
  execution/            batch fan-out substrate (discover → map → fan-out → aggregate)
  helpers/              small shared utilities
  tests/                black-box tests (unit + integration via testcontainers) + fixtures
  cli/                  dev/CI tooling module: lint, typecheck, test, coverage, check
python/
  techai_webutils/      the Python mirror of the Go substrate (published to PyPI)
```

Layers import downward only (`core → foundation → clients → … → transport`),
enforced by `depguard`. Every swappable component is `Kind` + `Config` +
`*_from_config`, selected by configuration and injected at a composition root.

## Consuming

Go:

```bash
go get github.com/gt-tech-ai/knowledge-engine@latest
```

```go
import "github.com/gt-tech-ai/knowledge-engine/go/foundation/logger"
```

Python:

```bash
pip install techai_webutils
```

## Developing

The `go/cli` tooling module drives the checks:

```bash
cd go/cli && go run . lint       # golangci-lint (layer rules) + ruff + eslint-free
go run . typecheck               # go vet + basedpyright
go run . test unit               # unit suite
go run . check                   # the static drift gates (structure, docs, comment-refs)
```

Integration tests use real dependencies in throwaway Docker containers
(testcontainers) — never fakes.

## License

MIT — see [LICENSE](LICENSE).
