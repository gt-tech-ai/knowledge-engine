package unit_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/jobs/connectorsync"
)

// TestSyncArgs_AdmissionGateContract tests that the connector-sync River job carries the admission-gate
// contract: the dedicated queue + per-connector uniqueness across the active states.
//
// Why this test is important:
//   - The global concurrency cap works only if every sync lands on the ONE connector-sync queue (the
//     worker caps that queue at K), and "max 1 sync per connector" holds only if the job is unique by
//     connector across the active states — a duplicate trigger or a backstop tick must dedup, not double
//     the work. These are deterministic properties of the job contract (the ≤K-concurrent behavior
//     itself is proven by the River+Postgres integration test).
//
// What it tests:
//   - Kind + Queue are the shared constants; InsertOpts is unique ByArgs over the active states,
//     excluding the terminal states so a new sync can run after the previous one finishes. Uniqueness
//     keys ONLY on the `river:"unique"`-tagged ConnectorID, so the per-trigger SyncID (which the worker
//     adopts) rides along without defeating per-connector dedup (proven at the DB layer in integration).
func TestSyncArgs_AdmissionGateContract(t *testing.T) {
	t.Parallel()

	args := connectorsync.SyncArgs{ConnectorID: uuid.New(), SyncID: uuid.New()}
	assert.Equal(t, connectorsync.Kind, args.Kind())
	assert.Equal(t, "connector_sync", args.Kind())

	opts := args.InsertOpts()
	assert.Equal(t, connectorsync.Queue, opts.Queue)
	assert.Equal(t, "connector-sync", opts.Queue)
	assert.True(
		t,
		opts.UniqueOpts.ByArgs,
		"unique by args → per-connector (only the river:\"unique\" ConnectorID is hashed)",
	)

	// Active states dedup an in-flight sync; the terminal states must NOT, so a new run can start
	// once the previous one has finished.
	require.NotEmpty(t, opts.UniqueOpts.ByState)
	assert.Contains(t, opts.UniqueOpts.ByState, rivertype.JobStateRunning)
	assert.Contains(t, opts.UniqueOpts.ByState, rivertype.JobStatePending)
	assert.NotContains(t, opts.UniqueOpts.ByState, rivertype.JobStateCompleted)
}

// TestReingestArgs_AdmissionGateContract tests the per-document connector-reingest River job contract
// it reuses the connector-sync queue and is unique per DOCUMENT across the active states.
//
// Why this test is important:
//   - The reingest reuses the connector-sync queue (no new infra), so it must carry that Queue constant.
//     Critically, uniqueness must be scoped to the ACTIVE (non-terminal) states: if the terminal states
//     were included, a document that finished one reingest and later FAILS AGAIN would be permanently
//     deduped and could never be retried — a silent, unrecoverable regression. This pins the active-only
//     window as a deterministic property of the job contract.
//
// What it tests:
//   - Kind + Queue are the shared constants; InsertOpts is unique ByArgs (per-document, only the
//     river:"unique" DocumentID is hashed) over the active states, EXCLUDING the terminal states so a
//     later legitimate retry of the same document re-enqueues rather than dedups.
func TestReingestArgs_AdmissionGateContract(t *testing.T) {
	t.Parallel()

	args := connectorsync.ReingestArgs{DocumentID: uuid.New()}
	assert.Equal(t, connectorsync.ReingestKind, args.Kind())
	assert.Equal(t, "connector_reingest", args.Kind())

	opts := args.InsertOpts()
	assert.Equal(t, connectorsync.Queue, opts.Queue)
	assert.Equal(t, "connector-sync", opts.Queue)
	assert.True(
		t,
		opts.UniqueOpts.ByArgs,
		"unique by args → per-document (only the river:\"unique\" DocumentID is hashed)",
	)

	// Active states dedup an in-flight reingest; the terminal states must NOT, so a document that fails
	// again after a completed reingest can be retried.
	require.NotEmpty(t, opts.UniqueOpts.ByState)
	assert.Contains(t, opts.UniqueOpts.ByState, rivertype.JobStateRunning)
	assert.Contains(t, opts.UniqueOpts.ByState, rivertype.JobStatePending)
	assert.NotContains(t, opts.UniqueOpts.ByState, rivertype.JobStateCompleted)
}
