package river

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine/go/core/events"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
)

// Compile-time interface assertion.
var _ interfaces.EventPublisher = (*EventPublisher)(nil)

// EventPublisher publishes events transactionally via River's outbox pattern.
// Phase 1: Stub. Phase 3: River transactional event publishing.
type EventPublisher struct {
	// enqueuer is the job enqueuer used for transactional event publishing.
	enqueuer interfaces.JobEnqueuer
}

// NewEventPublisher creates a new event publisher.
func NewEventPublisher(enqueuer interfaces.JobEnqueuer) *EventPublisher {
	return &EventPublisher{
		enqueuer: enqueuer,
	}
}

// Publish publishes an event.
// Phase 1: No-op. Phase 3: Transactional outbox via River.
func (p *EventPublisher) Publish(ctx context.Context, event events.Event) error {
	// Phase 1: Stub — no actual publishing
	// Phase 3: Insert event into outbox table via River transaction
	return nil
}

// PublishBatch publishes multiple events.
func (p *EventPublisher) PublishBatch(ctx context.Context, evts []events.Event) error {
	// Phase 1: Stub
	for _, event := range evts {
		if err := p.Publish(ctx, event); err != nil {
			return err
		}
	}
	return nil
}
