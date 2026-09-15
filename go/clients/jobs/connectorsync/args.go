// Package connectorsync is the shared River job contract for connector syncs
// It is the enqueue/dequeue seam between the API (which
// inserts a sync on TriggerSync / the poll backstop) and the dedicated
// connector-sync worker (which drains it) — an infrastructure envelope
// carrying only a connector id, so both apps import it downward from pkg
// without a lateral app→app dependency.
//
// The admission gate is two mechanisms working together: the worker
// configures the dedicated Queue with MaxWorkers=K (a PER-REPLICA cap — at
// most K syncs run at once per worker replica, so the fleet-wide ceiling is K
// × replicas; the rest queue = backpressure), and every SyncArgs is UNIQUE per
// connector across the active states (at most one sync per connector
// pending/running GLOBALLY, enforced by River's DB-backed unique key — the
// story's "max 1 concurrent sync per connector"). Duplicate triggers or a
// backstop tick while a sync is already in flight are deduped, not doubled.
package connectorsync

import (
	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// Queue is the dedicated River queue for connector syncs. The connector-sync
// worker configures it with MaxWorkers=K so K is the per-replica concurrency
// ceiling (fleet-wide = K × replicas), isolated from other workers' queues.
const Queue = "connector-sync"

// Kind is the River job kind for one connector sync run.
const Kind = "connector_sync"

// ReingestKind is the River job kind for re-ingesting a SINGLE failed
// connector document. A per-document retry cannot go through the
// document.uploaded outbox path (that consumer reads OUR documents bucket with
// no connector credentials); it must re-dispatch the one external object down
// the connector/bulk path. The API's RetryIngestion enqueues this; the
// connector-sync worker drains it, rebuilds the single BulkSyncObject from the
// document row + its connector, and writes it to the bulk lane. It reuses the
// connector-sync Queue so no new queue/SQS infra is introduced.
const ReingestKind = "connector_reingest"

// SyncArgs is the River job for one connector sync run. Uniqueness keys ONLY
// on ConnectorID (the `river:"unique"` tag) so a duplicate trigger while a
// sync is already pending/running for that connector is deduped, while the
// queue's MaxWorkers=K bounds per-replica concurrency (fleet-wide = K ×
// replicas). SyncID rides along untagged — it is the ConnectorSync history row
// the API's TriggerSync already created (so its returned sync_id is
// authoritative); the worker ADOPTS it rather than creating a second row.
// Because SyncID is not part of the uniqueness key, two triggers of the same
// connector still dedupe to one job even though each carries a distinct
// sync_id.
type SyncArgs struct {
	// TriggeredBy labels what enqueued the sync (e.g. a user action vs the
	// poll backstop); optional, carried for observability only.
	TriggeredBy string `json:"triggered_by,omitempty"`

	// ConnectorID is the connector to sync and the sole uniqueness key, so a
	// duplicate trigger while a sync is pending/running for it is deduped.
	ConnectorID uuid.UUID `json:"connector_id" river:"unique"`

	// SyncID is the ConnectorSync history row the API's TriggerSync already
	// created; the worker adopts it rather than creating a second row. Not
	// part of the uniqueness key, so two triggers still dedupe to one job.
	SyncID uuid.UUID `json:"sync_id"`
}

// Kind implements river.JobArgs.
func (SyncArgs) Kind() string { return Kind }

// InsertOpts implements river.JobArgsWithInsertOpts: the job lands on the
// dedicated connector-sync queue and is unique per connector across the ACTIVE
// states (available/pending/running/retryable/scheduled) — excluding the
// terminal states so a NEW sync can run once the previous one finishes. Only
// the `river:"unique"`-tagged ConnectorID is hashed into the uniqueness key,
// so the per-trigger SyncID never defeats the per-connector dedup.
func (SyncArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: Queue,
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByState: activeUniqueStates(),
		},
	}
}

// ReingestArgs is the River job for re-ingesting ONE failed connector document.
// Uniqueness keys ONLY on DocumentID (the `river:"unique"` tag) so a rapid double-retry of the same
// document is deduped to one in-flight job, while a DIFFERENT document's reingest runs concurrently
// (bounded by the queue's MaxWorkers). The worker resolves the document's connector + external object
// key from the document row (GetDocument), so the job envelope carries only the document id.
type ReingestArgs struct {
	// DocumentID is the failed connector document to re-ingest (the sole uniqueness key).
	DocumentID uuid.UUID `json:"document_id" river:"unique"`
}

// Kind implements river.JobArgs.
func (ReingestArgs) Kind() string { return ReingestKind }

// InsertOpts implements river.JobArgsWithInsertOpts: the reingest job reuses the connector-sync Queue
// (no new queue/SQS infra) and is unique per DOCUMENT across the same ACTIVE states as SyncArgs —
// excluding the terminal states, so once a reingest job finishes a document that fails AGAIN can be
// retried (a broader default window would permanently dedup the second, legitimate retry).
func (ReingestArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: Queue,
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByState: activeUniqueStates(),
		},
	}
}

// activeUniqueStates is the set of River job states the per-connector / per-document uniqueness key
// applies over: the ACTIVE (non-terminal) states only, so a completed job never blocks a later,
// legitimate re-trigger of the same connector/document.
func activeUniqueStates() []rivertype.JobState {
	return []rivertype.JobState{
		rivertype.JobStateAvailable,
		rivertype.JobStatePending,
		rivertype.JobStateRunning,
		rivertype.JobStateRetryable,
		rivertype.JobStateScheduled,
	}
}
