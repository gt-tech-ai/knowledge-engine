package events

// Mock generation directives for event interfaces.
// Run: go generate ./pkg/go/core/events
// All generated mocks are written to pkg/go/tests/mocks/.

//go:generate mockgen -destination=../../tests/mocks/mock_event.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/core/events Event
