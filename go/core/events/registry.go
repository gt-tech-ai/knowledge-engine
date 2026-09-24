package events

import (
	"encoding/json"
	"fmt"
	"sync"

	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// Factory returns a new, zero-valued event of one type, for Registry.Parse to
// decode a payload into.
type Factory func() Event

// Registry maps event types to factories, so a consumer parses its own event
// catalog: register each type once at the composition root, then Parse payloads.
// EventMetadata is the envelope every event shares; its event_type field selects
// the factory. A Registry is safe for concurrent use.
type Registry struct {
	// factories holds the registered factory per event type.
	factories map[EventType]Factory
	// mu guards factories.
	mu sync.RWMutex
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[EventType]Factory)}
}

// Register adds the factory for eventType. Registering a type twice, or a nil
// factory, returns a CodeInvalidInput error.
func (r *Registry) Register(eventType EventType, factory Factory) error {
	if factory == nil {
		return apperr.InvalidInput(fmt.Sprintf("nil factory for event type %q", eventType))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[eventType]; exists {
		return apperr.InvalidInput(fmt.Sprintf("event type %q already registered", eventType))
	}
	r.factories[eventType] = factory
	return nil
}

// Parse decodes a JSON event payload into the event registered for its
// event_type. A malformed payload or an unregistered type returns a
// CodeInvalidInput error. The payload is decoded twice (once to read the type,
// once into the concrete event); a consumer whose catalog is fixed and hot can
// keep a single-pass parser of its own.
func (r *Registry) Parse(data []byte) (Event, error) {
	var envelope struct {
		EventType EventType `json:"event_type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse event")
	}
	r.mu.RLock()
	factory, ok := r.factories[envelope.EventType]
	r.mu.RUnlock()
	if !ok {
		return nil, apperr.InvalidInput(fmt.Sprintf("unknown event type: %s", envelope.EventType))
	}
	event := factory()
	if err := json.Unmarshal(data, event); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse event")
	}
	return event, nil
}
