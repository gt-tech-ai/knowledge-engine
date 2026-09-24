// Package core_test verifies the go/core/events package: a consumer's event types parse through
// a Registry, and malformed or unregistered payloads are rejected. No network I/O occurs.
package unit_test

import (
	"encoding/json"
	"testing"

	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// widgetCreated is a consumer-owned event type, standing in for an application's own catalog.
type widgetCreated struct {
	events.EventMetadata

	WidgetID string `json:"widget_id"`
}

func (e *widgetCreated) Type() events.EventType         { return "widget.created" }
func (e *widgetCreated) Metadata() events.EventMetadata { return e.EventMetadata }

// newWidgetRegistry returns a registry with the consumer's widget.created event registered.
func newWidgetRegistry(t testing.TB) *events.Registry {
	t.Helper()
	reg := events.NewRegistry()
	require.NoError(t, reg.Register("widget.created", func() events.Event { return &widgetCreated{} }))
	return reg
}

// TestRegistryParsesConsumerEvents tests that a consumer parses its own event types through
// a registry instead of a closed catalog.
//
// Why this test is important:
//   - A library must not own an application's event catalog; the consumer registers its
//     types and the registry dispatches payloads to them.
//
// What it tests:
//   - A registered type parses into the consumer's concrete event with metadata and body.
//   - An unregistered type, a malformed payload, a duplicate registration and a nil factory
//     are each rejected with CodeInvalidInput.
func TestRegistryParsesConsumerEvents(t *testing.T) {
	t.Parallel()
	reg := newWidgetRegistry(t)

	evt, err := reg.Parse(
		[]byte(`{"event_id":"e1","event_type":"widget.created","org_id":"o1","widget_id":"w1"}`),
	)
	require.NoError(t, err)
	got, ok := evt.(*widgetCreated)
	require.True(t, ok, "want *widgetCreated, got %T", evt)
	assert.Equal(t, "w1", got.WidgetID)
	assert.Equal(t, "e1", got.Metadata().EventID)
	assert.Equal(t, "o1", got.Metadata().OrgID)

	_, err = reg.Parse([]byte(`{"event_type":"widget.deleted"}`))
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), "unregistered type: %v", err)
	_, err = reg.Parse([]byte(`{not json`))
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), "malformed payload: %v", err)
	err = reg.Register("widget.created", func() events.Event { return &widgetCreated{} })
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), "duplicate registration: %v", err)
	err = reg.Register("widget.updated", nil)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), "nil factory: %v", err)
}

// FuzzRegistryRoundTrip checks that any registered event survives marshal → Registry.Parse.
func FuzzRegistryRoundTrip(f *testing.F) {
	f.Add("e1", "o1", "w1")
	f.Add("", "", "")
	f.Add("é\"\\", "org/with/slash", "\x00")
	reg := newWidgetRegistry(f)

	f.Fuzz(func(t *testing.T, eventID, orgID, widgetID string) {
		in := &widgetCreated{
			EventMetadata: events.EventMetadata{
				EventID:   eventID,
				EventType: "widget.created",
				OrgID:     orgID,
			},
			WidgetID: widgetID,
		}
		data, err := json.Marshal(in)
		require.NoError(t, err)
		out, err := reg.Parse(data)
		require.NoError(t, err)
		// JSON replaces invalid UTF-8 with U+FFFD, so compare against the decoded form.
		var want widgetCreated
		require.NoError(t, json.Unmarshal(data, &want))
		assert.Equal(t, &want, out)
	})
}
