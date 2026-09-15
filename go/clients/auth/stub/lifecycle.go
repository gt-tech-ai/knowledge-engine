package stub

import (
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
)

// compile-time check: the stub provider is lifecycle-managed so a lifecycle.Manager
// registers it uniformly with the real backends in dev/test wiring. It embeds
// lifecycle.NoOp for Start/Stop: the in-memory stub holds all state in maps created
// by New, so there is no resource to open or close.
var _ interfaces.Lifecycle = (*Provider)(nil)
