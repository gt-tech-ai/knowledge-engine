// Package integration is the module root for go integration tests.
//
// Each sub-package exercises a specific layer against real infrastructure
// (PostgreSQL, Redis) via testcontainers:
//
//   - clients/   - Redis cache roundtrip over a real Redis container
//
// Ent-layer integration tests belong with the consumer that owns the Ent schema
// they exercise.
//
// All tests require the build tag "integration" and a Docker daemon.
// Run with: INTEGRATION=1 go test -tags=integration ./...
//
// TODO(phase3): Add integration tests for clients/messaging/sqs and
// clients/jobs/river once the real (non-stub) implementations land.
// Current Phase 1 stubs return nil without touching a broker or database,
// so an integration test would exercise no real behavior.
package integration
