package sqs

// Adapter provides AWS SQS-specific functionality.
// Phase 1: Stub. Phase 3: Watermill AWS SQS adapter configuration.
type Adapter struct {
	// cfg holds the SQS configuration for queue resolution.
	cfg Config
}

// NewAdapter creates a new SQS adapter.
func NewAdapter(cfg Config) *Adapter {
	return &Adapter{cfg: cfg}
}

// QueueURL returns the SQS queue URL for a topic.
func (a *Adapter) QueueURL(topic string) string {
	// Phase 1: Stub
	// Phase 3: Construct full SQS queue URL from config + topic
	return a.cfg.QueueURL + "/" + topic
}
