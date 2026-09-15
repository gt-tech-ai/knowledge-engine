package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine/go/core/events"
)

// EventPublisher publishes domain events, typically via a transactional outbox.
//
// Phase 1: Interface definition.
// Phase 3: River transactional event publishing.
type EventPublisher interface {
	// Publish publishes a single domain event.
	Publish(ctx context.Context, event events.Event) error

	// PublishBatch publishes multiple domain events.
	PublishBatch(ctx context.Context, events []events.Event) error
}
