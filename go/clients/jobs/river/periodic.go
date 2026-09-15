package river

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
)

// Compile-time interface assertion.
var _ interfaces.JobScheduler = (*Scheduler)(nil)

// Scheduler manages periodic jobs.
// Phase 1: Stub. Phase 3: River periodic job scheduling.
type Scheduler struct{}

// NewScheduler creates a new periodic job scheduler.
func NewScheduler() *Scheduler {
	return &Scheduler{}
}

// Schedule adds a periodic job to the schedule.
// Phase 1: No-op. Phase 3: River periodic job insertion.
func (s *Scheduler) Schedule(job interfaces.PeriodicJob, interval time.Duration) error {
	// Phase 1: Stub
	// Phase 3: Use River to schedule periodic job
	return nil
}
