package outboxtest

import (
	"context"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine/go/services/outbox"
)

// Routing is what RunRouting drives: a routing sink with healthy routes and one
// broken route, and a way to read what each route received.
type Routing struct {
	// Sink is the routing sink under test.
	Sink interfaces.OutboxSink
	// Delivered returns the payloads that arrived at route's destination.
	Delivered func(ctx context.Context, route string) ([]string, error)
	// Broken is a route whose destination fails every send.
	Broken string
	// Healthy are routes whose destinations accept sends.
	Healthy []string
}

// RunRouting checks that one relay over a routing sink isolates destinations:
// it enqueues three rows per healthy route and two on the broken route in one
// lane, runs the relay once, and requires every healthy route to receive exactly
// its own payloads while only the broken route's rows stay pending.
//
// Why this check matters:
//   - One relay drains many event types; a missing or failing destination must
//     fail only its own rows, never stall or misdeliver the others.
//
// What it asserts:
//   - each healthy route received exactly its three payloads;
//   - the lane ends with exactly the broken route's two rows pending, none parked.
func RunRouting(t *testing.T, h Harness, r Routing) {
	t.Helper()
	ctx, lane := context.Background(), newLane()
	want := map[string][]string{}
	enqueue := func(route string, n int) {
		for i := range n {
			payload := `{"route":"` + route + `","n":` + strconv.Itoa(i) + `}`
			rec := types.OutboxRecord{
				ID: uuid.New(), Lane: lane, Tenant: "org-1", Key: route, Payload: []byte(payload),
				Attributes: map[string]string{types.OutboxRouteAttribute: route},
				CreatedAt:  time.Now().Add(-time.Minute),
			}
			require.NoError(t, h.Enqueue(ctx, &rec))
			want[route] = append(want[route], payload)
		}
	}
	for _, route := range r.Healthy {
		enqueue(route, 3)
	}
	enqueue(r.Broken, 2)

	relay := outbox.NewRelay(outbox.DefaultRelayConfig(lane), h.Store, r.Sink, nil, nil, nil)
	require.NoError(t, relay.RunOnce(ctx))

	for _, route := range r.Healthy {
		got, err := r.Delivered(ctx, route)
		require.NoError(t, err)
		sort.Strings(got)
		require.Equalf(t, want[route], got, "route %s", route)
	}
	stats, err := h.Store.Stats(ctx, lane)
	require.NoError(t, err)
	require.Equal(t, 2, stats.Pending)
	require.Equal(t, 0, stats.Parked)
}
