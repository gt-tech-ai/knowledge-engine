// Package sqs provides an AWS-SDK-backed SQS messaging integration (publisher +
// subscriber) usable against real AWS SQS and against ElasticMQ for local dev.
package sqs

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
)

// Config holds SQS-specific messaging configuration.
type Config struct {
	// Logger logs subscriber receive/handle/delete errors (the subscriber runs
	// in a goroutine, so its errors have nowhere to return). Nil skips logging.
	Logger interfaces.Logger

	// Metrics, when set, records per-message handle counters + latency on the
	// subscriber via a handler-observability decorator. Nil skips metrics.
	Metrics interfaces.Metrics

	// API is an optional injected SQS client. When nil, NewPublisher/NewSubscriber
	// build the real aws-sdk-go-v2 client from the region/endpoint/credentials
	// below; a mock injected here exercises the messaging logic without a network.
	API API

	// AWSRegion is the AWS region where the SQS queues are provisioned.
	AWSRegion string

	// QueueURL is the legacy base queue URL retained for backward compatibility
	// with the factory and the Adapter; the real client resolves per-topic queue
	// URLs via ensureQueue (queue name → URL), so it is unused by Publisher/Subscriber.
	QueueURL string

	// Endpoint overrides the SQS service endpoint (e.g. "http://localhost:9324"
	// for ElasticMQ). Empty uses the default AWS endpoint for the region.
	Endpoint string

	// AccessKeyID is the static access key for local development (ElasticMQ).
	// Empty falls back to the default AWS credential chain (env, IAM role / IRSA,
	// shared credentials file).
	AccessKeyID string

	// SecretAccessKey is the static secret key paired with AccessKeyID. Ignored
	// when AccessKeyID is empty.
	SecretAccessKey string

	// BaseBackoff overrides the subscriber's initial receive-retry wait; 0 uses
	// the package default. Tests set a short value so the retry path runs fast.
	// (Ordered last: a non-pointer field, for struct field alignment.)
	BaseBackoff time.Duration
}
