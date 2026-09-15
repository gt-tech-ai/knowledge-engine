package auth0

import (
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
)

// compile-time check: the Auth0 provider is lifecycle-managed so a lifecycle.Manager
// registers it uniformly alongside the connection-oriented clients. It embeds
// lifecycle.NoOp for Start/Stop: the Auth0 Management API client is stateless over
// HTTPS (the M2M token is fetched and refreshed lazily on demand), so there is no
// persistent connection to open or close.
var _ interfaces.Lifecycle = (*Client)(nil)
