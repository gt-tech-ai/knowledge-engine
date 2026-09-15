package unit_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/messaging"
	"github.com/gt-tech-ai/knowledge-engine/go/clients/messaging/sqs"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/config/schema/infra"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/mocks"
)

// TestMessaging_NewFromConfig_AtTierRoot tests that the tier-root config factory
// builds a publisher from a foundation SQSConfig without a network round-trip.
//
// Why this test is important:
//   - NewFromConfig is the app-wiring entrypoint and now lives at the messaging
//     tier root (promoted from the sqs/ backend, mirroring the storage factory); a
//     broken translation of region/endpoint/credentials would fail services at
//     startup rather than at first publish.
//
// What it tests:
//   - messaging.NewFromConfig (not the backend) resolves a default (ElasticMQ)
//     SQSConfig to a non-nil MessagePublisher and no error.
func TestMessaging_NewFromConfig_AtTierRoot(t *testing.T) {
	t.Parallel()
	pub, err := messaging.NewFromConfig(
		context.Background(),
		messaging.KindSQS,
		infra.DefaultSQSConfig(),
	)
	require.NoError(t, err)
	require.NotNil(t, pub)
}

// recv reads one value from ch or fails the test after a bounded wait, so an
// async subscriber test's own progress governs its timing (no fixed sleeps).
func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		require.Failf(t, "timeout", "timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// receiveResult scripts one ReceiveMessage outcome: a delivered batch or an error.
type receiveResult struct {
	err   error
	batch []sqstypes.Message
}

// scriptedReceive returns each scripted result in order, then empty results
// forever, so the subscriber's poll loop delivers the batches once and then idles.
func scriptedReceive(
	results []receiveResult,
) func(context.Context, *awssqs.ReceiveMessageInput, ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	var mu sync.Mutex
	i := 0
	return func(
		_ context.Context, _ *awssqs.ReceiveMessageInput, _ ...func(*awssqs.Options),
	) (*awssqs.ReceiveMessageOutput, error) {
		mu.Lock()
		defer mu.Unlock()
		if i >= len(results) {
			return &awssqs.ReceiveMessageOutput{}, nil
		}
		r := results[i]
		i++
		if r.err != nil {
			return nil, r.err
		}
		return &awssqs.ReceiveMessageOutput{Messages: r.batch}, nil
	}
}

// scriptedGetQueueURL fails the first GetQueueUrl call with failFirst, then returns
// url on every subsequent call — so a test can exercise the ensure-queue error +
// backoff + recovery path.
func scriptedGetQueueURL(
	failFirst error,
	url string,
) func(context.Context, *awssqs.GetQueueUrlInput, ...func(*awssqs.Options)) (*awssqs.GetQueueUrlOutput, error) {
	var mu sync.Mutex
	n := 0
	return func(
		_ context.Context, _ *awssqs.GetQueueUrlInput, _ ...func(*awssqs.Options),
	) (*awssqs.GetQueueUrlOutput, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 && failFirst != nil {
			return nil, failFirst
		}
		return &awssqs.GetQueueUrlOutput{QueueUrl: aws.String(url)}, nil
	}
}

// TestSubscriber_LogsAndRecoversFromQueueResolutionError tests that a failed queue
// resolution is logged and retried rather than killing the consumer goroutine.
//
// Why this test is important:
//   - A not-yet-ready ElasticMQ at boot must not permanently disable the
//     subscription; the loop must log the error, back off, and re-resolve
//
// What it tests:
//   - When GetQueueUrl fails once, the error is logged via the configured logger
//     and the loop retries, then delivers the next message
func TestSubscriber_LogsAndRecoversFromQueueResolutionError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		DoAndReturn(scriptedGetQueueURL(errors.New("resolve failed"), "http://q")).
		AnyTimes()
	batch := []sqstypes.Message{{
		MessageId:     aws.String("m1"),
		Body:          aws.String("x"),
		ReceiptHandle: aws.String("rh-1"),
	}}
	api.EXPECT().ReceiveMessage(gomock.Any(), gomock.Any()).
		DoAndReturn(scriptedReceive([]receiveResult{{batch: batch}})).AnyTimes()
	api.EXPECT().DeleteMessageBatch(gomock.Any(), gomock.Any()).
		Return(&awssqs.DeleteMessageBatchOutput{}, nil).AnyTimes()
	spy := fixtures.NewSpyLogger()

	sub, err := sqs.NewSubscriber(sqs.Config{
		API: api, Logger: spy, BaseBackoff: time.Millisecond,
	})
	require.NoError(t, err)
	handled := make(chan struct{}, 1)
	require.NoError(t, sub.Subscribe(context.Background(), "q",
		func(_ context.Context, _ *interfaces.Message) error {
			handled <- struct{}{}
			return nil
		}))

	recv(t, handled, "handler after a queue-resolution retry")
	require.NoError(t, sub.Close())
	require.NotEmpty(t, spy.ErrorCalls, "queue resolution error should be logged")
}

// TestSubscriber_LogsDeleteFailure tests that a failed ack (DeleteMessageBatch) is
// logged without crashing the receive loop.
//
// Why this test is important:
//   - A failed ack means the message will redeliver; the operator must see the ack
//     failure, and the loop must keep running rather than die on it
//
// What it tests:
//   - When DeleteMessageBatch fails after a successful handle, the error is logged
func TestSubscriber_LogsDeleteFailure(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String("http://q")}, nil).
		AnyTimes()
	batch := []sqstypes.Message{{
		MessageId:     aws.String("m1"),
		Body:          aws.String("x"),
		ReceiptHandle: aws.String("rh-1"),
	}}
	api.EXPECT().ReceiveMessage(gomock.Any(), gomock.Any()).
		DoAndReturn(scriptedReceive([]receiveResult{{batch: batch}})).AnyTimes()
	api.EXPECT().DeleteMessageBatch(gomock.Any(), gomock.Any()).
		Return(nil, errors.New("delete failed")).AnyTimes()
	spy := fixtures.NewSpyLogger()

	sub, err := sqs.NewSubscriber(sqs.Config{
		API: api, Logger: spy, BaseBackoff: time.Millisecond,
	})
	require.NoError(t, err)
	handled := make(chan struct{}, 1)
	require.NoError(t, sub.Subscribe(context.Background(), "q",
		func(_ context.Context, _ *interfaces.Message) error {
			handled <- struct{}{}
			return nil
		}))

	recv(t, handled, "handler to be called")
	require.NoError(t, sub.Close())
	require.NotEmpty(t, spy.ErrorCalls, "delete failure should be logged")
}

// TestPublisher_ReturnsErrorWhenQueueUnresolvable tests that Publish surfaces a
// queue-resolution failure (a non-not-found SQS error) rather than sending blind.
//
// Why this test is important:
//   - A GetQueueUrl error that is not "queue missing" (e.g. access denied) must not
//     be swallowed or misinterpreted as a create-me signal
//
// What it tests:
//   - Publish returns an error when GetQueueUrl fails with a generic error
func TestPublisher_ReturnsErrorWhenQueueUnresolvable(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(nil, errors.New("access denied"))

	pub, err := sqs.NewPublisher(sqs.Config{API: api})
	require.NoError(t, err)

	require.Error(t, pub.Publish(context.Background(), "q", []byte("x")))
}

// TestPublisher_PublishBatch_StopsOnSendError tests that PublishBatch returns the
// first send error rather than continuing to send the rest of the batch.
//
// Why this test is important:
//   - A batch that swallowed a mid-batch failure would report success while having
//     dropped events
//
// What it tests:
//   - When SendMessageBatch fails at the transport level, PublishBatch returns that error
func TestPublisher_PublishBatch_StopsOnSendError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String("http://q")}, nil)
	api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).
		Return(nil, errors.New("send boom"))

	pub, err := sqs.NewPublisher(sqs.Config{API: api})
	require.NoError(t, err)

	require.Error(t, pub.PublishBatch(
		context.Background(), "q", [][]byte{[]byte("a"), []byte("b")},
	))
}

// TestSubscriber_StopsDuringBackoff tests that Close ends the receive loop while it
// is backing off after an error, rather than the loop ignoring cancellation.
//
// Why this test is important:
//   - A shutdown must drain promptly; a backoff that ignored context cancellation
//     would delay shutdown by the full backoff window
//
// What it tests:
//   - After a ReceiveMessage error puts the loop into backoff, Close cancels it and
//     the loop exits (Close returns)
func TestSubscriber_StopsDuringBackoff(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String("http://q")}, nil).
		AnyTimes()
	received := make(chan struct{}, 1)
	api.EXPECT().ReceiveMessage(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ *awssqs.ReceiveMessageInput, _ ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
			select {
			case received <- struct{}{}:
			default:
			}
			return nil, errors.New("transient")
		},
	).
		AnyTimes()

	sub, err := sqs.NewSubscriber(sqs.Config{
		API: api, BaseBackoff: 100 * time.Millisecond,
	})
	require.NoError(t, err)
	require.NoError(t, sub.Subscribe(context.Background(), "q",
		func(_ context.Context, _ *interfaces.Message) error { return nil }))

	recv(t, received, "first receive before backoff")
	// Close mid-backoff: the 100ms sleep must observe the cancellation and return.
	require.NoError(t, sub.Close())
}

// TestPublisher_ResolvesExistingQueueAndCaches tests that Publish resolves the
// queue URL by name, sends the body there, and caches the URL so a second publish
// to the same topic does not re-resolve.
//
// Why this test is important:
//   - Every cache-invalidation event flows through Publish; a wrong queue URL
//     silently drops the event, and re-resolving per publish would add a
//     GetQueueUrl round-trip to every grant write's hook
//
// What it tests:
//   - Publish sends the payload to the resolved queue URL
//   - The queue URL is resolved once (GetQueueUrl called once) and reused
func TestPublisher_ResolvesExistingQueueAndCaches(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	const url = "http://sqs.local/123/the-queue"
	// Times(1): resolving twice would mean the URL cache is broken.
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String(url)}, nil).Times(1)
	var sent []string
	api.EXPECT().SendMessage(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *awssqs.SendMessageInput, _ ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error) {
			sent = append(sent, aws.ToString(in.QueueUrl))
			return &awssqs.SendMessageOutput{}, nil
		},
	).
		Times(2)

	pub, err := sqs.NewPublisher(sqs.Config{API: api})
	require.NoError(t, err)

	require.NoError(t, pub.Publish(context.Background(), "the-queue", []byte("p1")))
	require.NoError(t, pub.Publish(context.Background(), "the-queue", []byte("p2")))

	require.Equal(t, []string{url, url}, sent, "both sends go to the resolved URL")
}

// TestPublisher_CreatesQueueWhenMissing tests that a not-found queue is created
// (idempotent ensureQueue) and the created URL is used.
//
// Why this test is important:
//   - ElasticMQ boots with zero queues; the first publisher to run must create the
//     queue rather than fail, or invalidation never starts working locally
//
// What it tests:
//   - When GetQueueUrl reports the queue is missing, CreateQueue is called and the
//     message is sent to the created URL
func TestPublisher_CreatesQueueWhenMissing(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(nil, &sqstypes.QueueDoesNotExist{})
	const created = "http://sqs.local/123/new-queue"
	api.EXPECT().CreateQueue(gomock.Any(), gomock.Any()).
		Return(&awssqs.CreateQueueOutput{QueueUrl: aws.String(created)}, nil)
	var sentURL string
	api.EXPECT().SendMessage(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *awssqs.SendMessageInput, _ ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error) {
			sentURL = aws.ToString(in.QueueUrl)
			return &awssqs.SendMessageOutput{}, nil
		},
	)

	pub, err := sqs.NewPublisher(sqs.Config{API: api})
	require.NoError(t, err)

	require.NoError(t, pub.Publish(context.Background(), "new-queue", []byte("payload")))
	require.Equal(t, created, sentURL, "message sent to the created queue URL")
}

// TestPublisher_PropagatesSendError tests that a send failure is surfaced to the
// caller rather than swallowed.
//
// Why this test is important:
//   - The emitter relies on Publish's error to log a failed invalidation; a
//     swallowed send error would hide a broker outage
//
// What it tests:
//   - A SendMessage failure is returned from Publish
func TestPublisher_PropagatesSendError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String("http://q")}, nil)
	api.EXPECT().SendMessage(gomock.Any(), gomock.Any()).
		Return(nil, errors.New("sqs unavailable"))

	pub, err := sqs.NewPublisher(sqs.Config{API: api})
	require.NoError(t, err)

	require.Error(t, pub.Publish(context.Background(), "q", []byte("payload")))
}

// TestSubscriber_DeliversAndAcks tests that a received message is handed to the
// handler and deleted (acked) when the handler returns nil.
//
// Why this test is important:
//   - This is the receive→handle→ack happy path; a missing delete leaves the
//     message to redeliver forever, and a missing handler call drops the event
//
// What it tests:
//   - The handler receives the message body and topic
//   - The message's receipt handle is passed to DeleteMessage
func TestSubscriber_DeliversAndAcks(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String("http://q")}, nil).
		AnyTimes()
	batch := []sqstypes.Message{{
		MessageId:     aws.String("m1"),
		Body:          aws.String("hello"),
		ReceiptHandle: aws.String("rh-1"),
	}}
	api.EXPECT().ReceiveMessage(gomock.Any(), gomock.Any()).
		DoAndReturn(scriptedReceive([]receiveResult{{batch: batch}})).AnyTimes()
	deleted := make(chan string, 1)
	api.EXPECT().DeleteMessageBatch(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *awssqs.DeleteMessageBatchInput, _ ...func(*awssqs.Options)) (*awssqs.DeleteMessageBatchOutput, error) {
			deleted <- aws.ToString(in.Entries[0].ReceiptHandle)
			return &awssqs.DeleteMessageBatchOutput{}, nil
		},
	).
		AnyTimes()

	sub, err := sqs.NewSubscriber(sqs.Config{API: api, BaseBackoff: time.Millisecond})
	require.NoError(t, err)
	got := make(chan *interfaces.Message, 1)
	require.NoError(t, sub.Subscribe(context.Background(), "q",
		func(_ context.Context, m *interfaces.Message) error {
			got <- m
			return nil
		}))

	msg := recv(t, got, "handler to be called")
	require.Equal(t, "hello", string(msg.Payload))
	require.Equal(t, "q", msg.Topic)
	require.Equal(t, "rh-1", recv(t, deleted, "acked message to be deleted"))
	require.NoError(t, sub.Close())
}

// TestSubscriber_HandlerError_DoesNotDelete tests that a handler error leaves the
// message on the queue (nack → redelivery) rather than deleting it.
//
// Why this test is important:
//   - A transient invalidation failure must be retried; deleting on error would
//     drop the cache-bust and leave a stale entry until the TTL
//
// What it tests:
//   - When the handler returns an error, DeleteMessage is never called
func TestSubscriber_HandlerError_DoesNotDelete(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String("http://q")}, nil).
		AnyTimes()
	batch := []sqstypes.Message{{
		MessageId:     aws.String("m1"),
		Body:          aws.String("boom"),
		ReceiptHandle: aws.String("rh-1"),
	}}
	api.EXPECT().ReceiveMessage(gomock.Any(), gomock.Any()).
		DoAndReturn(scriptedReceive([]receiveResult{{batch: batch}})).AnyTimes()
	// Times(0): a nacked message must never be deleted.
	api.EXPECT().DeleteMessageBatch(gomock.Any(), gomock.Any()).Times(0)

	sub, err := sqs.NewSubscriber(sqs.Config{API: api, BaseBackoff: time.Millisecond})
	require.NoError(t, err)
	handled := make(chan struct{}, 1)
	require.NoError(t, sub.Subscribe(context.Background(), "q",
		func(_ context.Context, _ *interfaces.Message) error {
			handled <- struct{}{}
			return errors.New("transient failure")
		}))

	recv(t, handled, "handler to be called")
	require.NoError(t, sub.Close())
}

// TestSubscriber_RetriesAfterReceiveError tests that a transient ReceiveMessage
// error does not kill the consumer goroutine — it backs off and keeps polling.
//
// Why this test is important:
//   - If the receive loop exits on the first transient error, invalidation is
//     silently disabled for the process lifetime (masked by the TTL forever)
//
// What it tests:
//   - After ReceiveMessage errors once, the loop retries and still delivers the
//     next message
func TestSubscriber_RetriesAfterReceiveError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String("http://q")}, nil).
		AnyTimes()
	batch := []sqstypes.Message{{
		MessageId:     aws.String("m1"),
		Body:          aws.String("after-retry"),
		ReceiptHandle: aws.String("rh-1"),
	}}
	api.EXPECT().ReceiveMessage(gomock.Any(), gomock.Any()).DoAndReturn(
		scriptedReceive([]receiveResult{
			{err: errors.New("transient receive error")},
			{batch: batch},
		}),
	).AnyTimes()
	api.EXPECT().DeleteMessageBatch(gomock.Any(), gomock.Any()).
		Return(&awssqs.DeleteMessageBatchOutput{}, nil).AnyTimes()

	sub, err := sqs.NewSubscriber(sqs.Config{API: api, BaseBackoff: time.Millisecond})
	require.NoError(t, err)
	handled := make(chan struct{}, 1)
	require.NoError(t, sub.Subscribe(context.Background(), "q",
		func(_ context.Context, _ *interfaces.Message) error {
			handled <- struct{}{}
			return nil
		}))

	recv(t, handled, "handler to be called after a receive error")
	require.NoError(t, sub.Close())
}

// TestPublisher_PublishBatch_SendsEachPayload tests that PublishBatch resolves the
// queue and sends every payload.
//
// Why this test is important:
//   - A batch publish that drops payloads would silently lose invalidation events
//
// What it tests:
//   - PublishBatch resolves the queue once and issues a single SendMessageBatch
//     carrying both payloads (not one SendMessage per payload)
func TestPublisher_PublishBatch_SendsEachPayload(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String("http://q")}, nil)
	api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *awssqs.SendMessageBatchInput, _ ...func(*awssqs.Options)) (*awssqs.SendMessageBatchOutput, error) {
			require.Len(t, in.Entries, 2, "both payloads in one batch")
			return &awssqs.SendMessageBatchOutput{}, nil
		},
	).
		Times(1)

	pub, err := sqs.NewPublisher(sqs.Config{API: api})
	require.NoError(t, err)

	require.NoError(t, pub.PublishBatch(
		context.Background(), "q", [][]byte{[]byte("a"), []byte("b")},
	))
}

// TestPublisher_PublishBatch_ChunksInto10s tests that PublishBatch groups payloads
// into SendMessageBatch requests of at most maxBatchSize (#8) instead of one
// SendMessage per payload.
//
// Why this test is important:
//   - The batch relay publishes many events at once; N single SendMessage calls
//     is N round-trips — batching cuts that to ceil(N/10) and is the throughput win
//
// What it tests:
//   - 25 payloads issue exactly three SendMessageBatch calls sized 10, 10, 5
func TestPublisher_PublishBatch_ChunksInto10s(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String("http://q")}, nil)
	var sizes []int
	api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *awssqs.SendMessageBatchInput, _ ...func(*awssqs.Options)) (*awssqs.SendMessageBatchOutput, error) {
			sizes = append(sizes, len(in.Entries))
			return &awssqs.SendMessageBatchOutput{}, nil
		},
	).
		Times(3)

	pub, err := sqs.NewPublisher(sqs.Config{API: api})
	require.NoError(t, err)

	payloads := make([][]byte, 25)
	for i := range payloads {
		payloads[i] = []byte(fmt.Sprintf("m%d", i))
	}
	require.NoError(t, pub.PublishBatch(context.Background(), "q", payloads))
	require.Equal(t, []int{10, 10, 5}, sizes, "25 payloads chunk into 10+10+5")
}

// TestPublisher_PublishBatch_SurfacesFailedEntries tests that a partial batch
// failure (SendMessageBatchOutput.Failed) is surfaced as an error naming the
// failed entries (#8), rather than being silently dropped.
//
// Why this test is important:
//   - SendMessageBatch can partially succeed; swallowing the Failed list would
//     report success while losing events, the exact failure mode batching risks
//
// What it tests:
//   - When the response reports a failed entry, PublishBatch errors and the error
//     names the entry id and service code
func TestPublisher_PublishBatch_SurfacesFailedEntries(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String("http://q")}, nil)
	api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).Return(
		&awssqs.SendMessageBatchOutput{
			Failed: []sqstypes.BatchResultErrorEntry{
				{Id: aws.String("1"), Code: aws.String("InternalError")},
			},
		}, nil,
	)

	pub, err := sqs.NewPublisher(sqs.Config{API: api})
	require.NoError(t, err)

	err = pub.PublishBatch(context.Background(), "q", [][]byte{[]byte("a"), []byte("b")})
	require.Error(t, err)
	require.Contains(t, err.Error(), "1:InternalError", "failed entry id+code surfaced")
}

// TestSubscriber_FansOutBatchAndBatchDeletes tests that a receive batch is handled
// concurrently (#7) and the handled subset is acked in a single DeleteMessageBatch
// (O6).
//
// Why this test is important:
//   - Sequential per-message handling caps consume throughput at one handler at a
//     time; fanning the batch out with a batched delete is the consumer-side win
//
// What it tests:
//   - All 10 messages of a batch are handled concurrently (a barrier that only
//     clears if they overlap), and their acks arrive as one 10-entry DeleteMessageBatch
func TestSubscriber_FansOutBatchAndBatchDeletes(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String("http://q")}, nil).
		AnyTimes()

	const n = 10
	batch := make([]sqstypes.Message, n)
	for i := range batch {
		batch[i] = sqstypes.Message{
			MessageId:     aws.String(fmt.Sprintf("m%d", i)),
			Body:          aws.String("x"),
			ReceiptHandle: aws.String(fmt.Sprintf("rh-%d", i)),
		}
	}
	api.EXPECT().ReceiveMessage(gomock.Any(), gomock.Any()).
		DoAndReturn(scriptedReceive([]receiveResult{{batch: batch}})).AnyTimes()
	deletedCount := make(chan int, 1)
	api.EXPECT().DeleteMessageBatch(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *awssqs.DeleteMessageBatchInput, _ ...func(*awssqs.Options)) (*awssqs.DeleteMessageBatchOutput, error) {
			select {
			case deletedCount <- len(in.Entries):
			default:
			}
			return &awssqs.DeleteMessageBatchOutput{}, nil
		},
	).
		AnyTimes()

	var inFlight sync.WaitGroup
	inFlight.Add(n)
	release := make(chan struct{})

	sub, err := sqs.NewSubscriber(sqs.Config{API: api, BaseBackoff: time.Millisecond})
	require.NoError(t, err)
	require.NoError(t, sub.Subscribe(context.Background(), "q",
		func(_ context.Context, _ *interfaces.Message) error {
			inFlight.Done() // signal this handler is in-flight
			<-release       // hold so all n overlap — sequential handling never clears the barrier
			return nil
		}))

	done := make(chan struct{})
	go func() { inFlight.Wait(); close(done) }()
	recv(t, done, "all 10 handlers concurrently in-flight (fan-out)")
	close(release)

	require.Equal(
		t,
		n,
		recv(t, deletedCount, "handled subset acked in one DeleteMessageBatch"),
	)
	require.NoError(t, sub.Close())
}

// TestPublisher_PublishBatch_EmptyIsNoOp tests that PublishBatch with no payloads
// returns nil without resolving the queue or calling the API.
//
// Why this test is important:
//   - The batch relay may flush an empty buffer; that must be a cheap no-op, not a
//     needless queue resolution or a spurious empty SendMessageBatch
//
// What it tests:
//   - PublishBatch(nil) returns nil and makes no API calls
func TestPublisher_PublishBatch_EmptyIsNoOp(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl) // no expectations: any call fails the test
	pub, err := sqs.NewPublisher(sqs.Config{API: api})
	require.NoError(t, err)

	require.NoError(t, pub.PublishBatch(context.Background(), "q", nil))
}

// TestPublisher_PublishBatch_QueueResolveError tests that PublishBatch surfaces a
// queue-resolution failure rather than attempting to send.
//
// Why this test is important:
//   - If the queue can't be resolved, the batch must fail loudly, not silently drop
//
// What it tests:
//   - When GetQueueUrl fails, PublishBatch returns the error and never sends
func TestPublisher_PublishBatch_QueueResolveError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(nil, errors.New("access denied"))

	pub, err := sqs.NewPublisher(sqs.Config{API: api})
	require.NoError(t, err)

	require.Error(t, pub.PublishBatch(context.Background(), "q", [][]byte{[]byte("a")}))
}

// TestSubscriber_LogsBatchDeletePartialFailure tests that a per-entry failure in
// the DeleteMessageBatch response is logged (the message will redeliver) without
// crashing the receive loop.
//
// Why this test is important:
//   - DeleteMessageBatch can partially fail; an unlogged per-entry failure hides a
//     redelivery the operator needs to see
//
// What it tests:
//   - When DeleteMessageBatch returns a Failed entry, the error is logged
func TestSubscriber_LogsBatchDeletePartialFailure(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String("http://q")}, nil).
		AnyTimes()
	batch := []sqstypes.Message{{
		MessageId:     aws.String("m1"),
		Body:          aws.String("x"),
		ReceiptHandle: aws.String("rh-1"),
	}}
	api.EXPECT().ReceiveMessage(gomock.Any(), gomock.Any()).
		DoAndReturn(scriptedReceive([]receiveResult{{batch: batch}})).AnyTimes()
	api.EXPECT().DeleteMessageBatch(gomock.Any(), gomock.Any()).Return(
		&awssqs.DeleteMessageBatchOutput{
			Failed: []sqstypes.BatchResultErrorEntry{
				{Id: aws.String("0"), Code: aws.String("ReceiptHandleIsInvalid")},
			},
		}, nil,
	).AnyTimes()
	spy := fixtures.NewSpyLogger()

	sub, err := sqs.NewSubscriber(sqs.Config{
		API: api, Logger: spy, BaseBackoff: time.Millisecond,
	})
	require.NoError(t, err)
	handled := make(chan struct{}, 1)
	require.NoError(t, sub.Subscribe(context.Background(), "q",
		func(_ context.Context, _ *interfaces.Message) error {
			handled <- struct{}{}
			return nil
		}))

	recv(t, handled, "handler to be called")
	require.NoError(t, sub.Close())
	require.NotEmpty(t, spy.ErrorCalls, "batch-delete partial failure should be logged")
}
