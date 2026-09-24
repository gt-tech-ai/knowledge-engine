package s3

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	coreerrors "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// EnsureBucket creates the named bucket if it does not already exist,
// idempotently: it probes with HeadBucket, creates the bucket when absent, and
// tolerates the already-exists/already-owned races so concurrent callers both
// succeed. It exists so local/dev provisioning — e.g. a seeding tool —
// can guarantee the document-upload path has a bucket to write to, rather than
// surfacing NoSuchBucket only on the first presigned PUT.
func (c *s3Client) EnsureBucket(ctx context.Context, bucket string) error {
	if _, err := c.api.HeadBucket(
		ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)},
	); err == nil {
		return nil // already exists and reachable
	}

	if _, err := c.api.CreateBucket(
		ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)},
	); err != nil {
		var owned *s3types.BucketAlreadyOwnedByYou
		var exists *s3types.BucketAlreadyExists
		if coreerrors.As(err, &owned) || coreerrors.As(err, &exists) {
			return nil // created concurrently — idempotent success
		}
		return coreerrors.Wrap(
			err,
			coreerrors.CodeInternal,
			fmt.Sprintf("create bucket %q", bucket),
		)
	}
	return nil
}
