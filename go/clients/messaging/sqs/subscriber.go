package sqs

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/messaging/decorators"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/lifecycle"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/resilience/retry"
)

// Compile-time interface assertion.
var _ interfaces.MessageConsumer = (*Subscriber)(nil)

const (
	// defaultBaseBackoff is the initial wait after a transient error before the
	// receive loop retries; it doubles up to maxBackoff.
	defaultBaseBackoff = 500 * time.Millisecond
	// maxBackoff caps the retry wait so a long outage does not stall recovery.
	maxBackoff = 10 * time.Second
	// longPollSeconds is the SQS long-poll wait, which lowers empty-receive churn.
	longPollSeconds = 10
	// maxMessagesPerReceive is the batch size per ReceiveMessage.
	maxMessagesPerReceive = 10
	// maxConcurrentHandlers bounds how many messages from one receive batch are
	// handled concurrently. The batch is capped at maxMessagesPerReceive, so this
	// fans the whole batch out while keeping the live goroutine count bounded.
	maxConcurrentHandlers = maxMessagesPerReceive
	// handlerGracePeriod bounds an in-flight handler+ack so a shutdown drain can't
	// hang on a stuck message; it runs on a context decoupled from loop cancellation.
	handlerGracePeriod = 15 * time.Second
)

// Subscriber consumes SQS messages via the AWS SDK. Each Subscribe call starts a
// long-poll goroutine that resolves (and creates, if absent) the queue, then
// loops receive → handle → delete. Transient errors are logged and retried with
// backoff — the loop never exits on them — so invalidation cannot be silently
// disabled by a momentary outage. Close cancels all subscriptions and waits for
// in-flight handlers to drain.
type Subscriber struct {
	// NoOp supplies the no-op Start/Stop: the long-poll receive loop is owned per-Subscribe
	// call (cancelled via cancels), so the Subscriber itself opens/closes no shared resource.
	lifecycle.NoOp

	// api is the SQS API used for receive/delete (the real SDK client or a test double).
	api API
	// logger reports receive/handle errors surfaced by the poll goroutines.
	logger interfaces.Logger
	// obs wraps each handler with per-message logging + metrics.
	obs *decorators.HandlerObservability
	// cancels holds the cancel func for each Subscribe's poll goroutine, cancelled by Close.
	cancels []context.CancelFunc
	// wg tracks the poll goroutines so Close can wait for in-flight handlers to drain.
	wg sync.WaitGroup
	// baseBackoff is the initial receive-retry wait for transient errors.
	baseBackoff time.Duration
	// mu guards cancels against concurrent Subscribe/Close.
	mu sync.Mutex
}

// NewSubscriber creates an SQS message subscriber. It uses Config.API when set,
// otherwise builds the real AWS SDK client from cfg. Config.BaseBackoff overrides
// the initial receive-retry wait (0 uses the package default).
func NewSubscriber(cfg Config) (*Subscriber, error) {
	api, err := resolveAPI(cfg)
	if err != nil {
		return nil, err
	}
	baseBackoff := cfg.BaseBackoff
	if baseBackoff <= 0 {
		baseBackoff = defaultBaseBackoff
	}
	return &Subscriber{
		api:         api,
		logger:      cfg.Logger,
		obs:         decorators.NewHandlerObservability(cfg.Logger, cfg.Metrics),
		baseBackoff: baseBackoff,
	}, nil
}

// Subscribe starts a long-poll consumer goroutine for topic (an SQS queue name).
// It returns immediately; the goroutine runs until Close (or the passed context)
// cancels it.
func (s *Subscriber) Subscribe(
	ctx context.Context,
	topic string,
	handler interfaces.MessageHandler,
) error {
	// Per-message observability (handle logging + metrics) is applied as a composed
	// handler decorator; the receive loop's own infra resilience + error logging
	// (queue resolution, receive, ack) stays intrinsic to consume.
	handler = s.obs.Wrap(handler)

	//nolint:gosec // cancel is stored in s.cancels and invoked by Close (drains the loop).
	loopCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancels = append(s.cancels, cancel)
	s.mu.Unlock()

	s.wg.Add(1)
	go s.consume(loopCtx, topic, handler)
	return nil
}

// Close cancels every subscription and waits for in-flight handlers to drain.
func (s *Subscriber) Close() error {
	s.mu.Lock()
	for _, cancel := range s.cancels {
		cancel()
	}
	s.cancels = nil
	s.mu.Unlock()
	s.wg.Wait()
	return nil
}

// consume runs the receive → handle → delete loop for one topic until ctx is
// done. It resolves the queue lazily (retrying on failure) so a not-yet-ready
// ElasticMQ at boot does not permanently disable the subscription.
func (s *Subscriber) consume(
	ctx context.Context,
	topic string,
	handler interfaces.MessageHandler,
) {
	defer s.wg.Done()

	var queueURL string
	backoff := s.baseBackoff
	for {
		if ctx.Err() != nil {
			return
		}

		if queueURL == "" {
			url, err := ensureQueue(ctx, s.api, topic)
			if err != nil {
				if !s.backoffOrDone(ctx, "ensure queue", topic, err, &backoff) {
					return
				}
				continue
			}
			queueURL = url
			backoff = s.baseBackoff
		}

		out, err := s.api.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queueURL),
			MaxNumberOfMessages: maxMessagesPerReceive,
			WaitTimeSeconds:     longPollSeconds,
		})
		if err != nil {
			if !s.backoffOrDone(ctx, "receive", topic, err, &backoff) {
				return
			}
			continue
		}
		backoff = s.baseBackoff

		s.handleBatch(ctx, topic, queueURL, handler, out.Messages)
	}
}

// handleBatch fans the messages of one receive batch out to the handler with
// bounded concurrency (≤ maxConcurrentHandlers live goroutines), then acks the
// successfully-handled subset in a single DeleteMessageBatch (#7, O6). Handlers
// run on grace-bounded contexts decoupled from the loop's cancellation, so an
// in-flight batch drains when Close cancels the receive loop rather than aborting
// mid-flight.
func (s *Subscriber) handleBatch(
	ctx context.Context,
	topic, queueURL string,
	handler interfaces.MessageHandler,
	messages []sqstypes.Message,
) {
	if len(messages) == 0 {
		return
	}
	var (
		mu      sync.Mutex
		handled []sqstypes.Message
		wg      sync.WaitGroup
	)
	sem := make(chan struct{}, maxConcurrentHandlers)
	for _, m := range messages {
		// Acquire a slot before spawning so at most maxConcurrentHandlers
		// goroutines are live at once (bounded fan-out).
		sem <- struct{}{}
		wg.Add(1)
		go func(m sqstypes.Message) {
			defer wg.Done()
			defer func() { <-sem }()
			if s.runHandler(ctx, topic, handler, m) {
				mu.Lock()
				handled = append(handled, m)
				mu.Unlock()
			}
		}(m)
	}
	wg.Wait()
	s.deleteHandled(ctx, topic, queueURL, handled)
}

// runHandler dispatches one message to the handler on a grace-bounded context
// decoupled from the loop's cancellation, and reports whether it was handled
// successfully (and so should be acked). A handler error leaves the message for
// redelivery (nack); it is logged by the handler observability decorator.
func (s *Subscriber) runHandler(
	ctx context.Context,
	topic string,
	handler interfaces.MessageHandler,
	m sqstypes.Message,
) bool {
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handlerGracePeriod)
	defer cancel()
	msg := &interfaces.Message{
		ID:      aws.ToString(m.MessageId),
		Topic:   topic,
		Payload: []byte(aws.ToString(m.Body)),
	}
	return handler(hctx, msg) == nil
}

// deleteHandled acks the handled subset of a receive batch in one
// DeleteMessageBatch (O6). The receive batch is capped at maxMessagesPerReceive
// (≤ maxBatchSize), so no chunking is needed. A batch-level error or any
// per-entry failure is logged — the affected message simply redelivers.
func (s *Subscriber) deleteHandled(
	ctx context.Context,
	topic, queueURL string,
	handled []sqstypes.Message,
) {
	if len(handled) == 0 {
		return
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handlerGracePeriod)
	defer cancel()
	entries := make([]sqstypes.DeleteMessageBatchRequestEntry, len(handled))
	for i, m := range handled {
		entries[i] = sqstypes.DeleteMessageBatchRequestEntry{
			Id:            aws.String(strconv.Itoa(i)),
			ReceiptHandle: m.ReceiptHandle,
		}
	}
	out, err := s.api.DeleteMessageBatch(dctx, &awssqs.DeleteMessageBatchInput{
		QueueUrl: aws.String(queueURL),
		Entries:  entries,
	})
	if err != nil {
		s.logError("delete batch", topic, err)
		return
	}
	if len(out.Failed) > 0 {
		s.logError(
			"delete batch", topic,
			failedEntriesError("sqs delete message batch", out.Failed),
		)
	}
}

// backoffOrDone logs the error and sleeps for the current backoff, doubling it
// up to maxBackoff. Returns false if the context was cancelled.
func (s *Subscriber) backoffOrDone(
	ctx context.Context,
	op, topic string,
	err error,
	backoff *time.Duration,
) bool {
	if ctx.Err() != nil {
		return false
	}
	s.logError(op, topic, err)
	// Sleep the current backoff with equal jitter so multiple subscribers
	// recovering from the same outage don't retry in lock-step (R5).
	if !sleepCtx(ctx, retry.EqualJitter(*backoff)) {
		return false
	}
	*backoff *= 2
	if *backoff > maxBackoff {
		*backoff = maxBackoff
	}
	return true
}

// logError logs a subscriber error when a logger is configured.
func (s *Subscriber) logError(op, topic string, err error) {
	if s.logger != nil {
		s.logger.Error("sqs subscriber error", "op", op, "topic", topic, "error", err)
	}
}

// sleepCtx sleeps for d or until ctx is done. Returns false if ctx was cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
