package s3

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/smithy-go"

	coreerr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
)

// testConnectionMaxKeys bounds the reachability probe — a small count, not a
// full inventory (bulk listing + ingestion is the Python/Ray side).
const testConnectionMaxKeys = 1000

// S3ClientBuilder builds a connector-scoped StorageClient that signs with the
// given credentials in the given region. The APP composition root supplies it
// (a closure over the storage tier's client construction, capturing the
// app-infra endpoint — MinIO in dev, none in prod), so
// go/clients/connector NEVER imports go/clients/storage — the layering
// seam that avoids a lateral same-layer import.
type S3ClientBuilder func(
	ctx context.Context,
	creds aws.CredentialsProvider,
	region string,
) (interfaces.StorageClient, error)

// Source is the S3 ConnectorSource over a connector-scoped StorageClient + a
// bucket/prefix.
type Source struct {
	// storage is the connector-scoped S3 storage client.
	storage interfaces.StorageClient

	// bucket is the S3 bucket the source reads from.
	bucket string

	// prefix restricts listing/reads to keys under this bucket prefix.
	prefix string
}

// Ensure Source satisfies the ConnectorSource contract.
var _ interfaces.ConnectorSource = (*Source)(nil)

// NewSource builds an S3 ConnectorSource for cfg: it selects the credential
// strategy, has the injected builder construct a connector-scoped
// StorageClient signing with those credentials, and wraps it with the
// connector's bucket/prefix. An unknown auth method fails loud
// (CodeInvalidInput).
func NewSource(
	ctx context.Context,
	cfg interfaces.SourceConfig,
	stsClient stscreds.AssumeRoleAPIClient,
	build S3ClientBuilder,
) (*Source, error) {
	creds, err := credentialsFromConfig(
		cfg.AuthMethod,
		cfg.IAMRoleARN,
		cfg.ExternalID,
		cfg.AccessKeyID,
		cfg.SecretAccessKey,
		stsClient,
	)
	if err != nil {
		return nil, err
	}
	client, err := build(ctx, creds, cfg.Region)
	if err != nil {
		return nil, coreerr.Wrap(err, coreerr.CodeInternal, "connector s3: build client")
	}
	return &Source{storage: client, bucket: cfg.Bucket, prefix: cfg.Prefix}, nil
}

// TestConnection lists a bounded page under the connector's bucket/prefix and
// returns the object count on that page — a reachability + credential probe,
// not a full inventory (the exhaustive crawl is ListPage). It classifies a
// failure via classifyListError.
func (s *Source) TestConnection(ctx context.Context) (int, error) {
	objs, err := s.storage.ListObjectsPage(ctx, s.bucket, s.prefix, testConnectionMaxKeys)
	if err != nil {
		return 0, classifyListError(err)
	}
	return len(objs), nil
}

// ListPage streams one page under the connector's bucket/prefix starting after
// continuationToken, returning the page + the token to resume from ("" when
// exhausted). It is the memory-bounded, checkpointable crawl the sync engine
// pages a large bucket with; a failure is classified (transient vs terminal)
// exactly like TestConnection so the worker can decide retry-vs-fail.
func (s *Source) ListPage(
	ctx context.Context,
	continuationToken string,
	limit int,
) ([]interfaces.StorageObject, string, error) {
	objs, next, err := s.storage.ListObjectsPageToken(
		ctx, s.bucket, s.prefix, continuationToken, limit,
	)
	if err != nil {
		return nil, "", classifyListError(err)
	}
	return objs, next, nil
}

// classifyListError maps a TestConnection/ListPage (ListObjectsPage or
// ListObjectsPageToken) failure to a coded connector error. An AWS
// CLIENT-fault API error (4xx: NoSuchBucket, AccessDenied, InvalidAccessKeyId,
// region redirect) means the connector's config/credentials are wrong →
// CodeInvalidInput; anything else (a server-fault 5xx, or a non-API
// transport/dial/timeout error) is transient → CodeUnavailable.
func classifyListError(err error) error {
	var apiErr smithy.APIError
	if coreerr.As(err, &apiErr) && apiErr.ErrorFault() == smithy.FaultClient {
		return coreerr.Wrap(
			err,
			coreerr.CodeInvalidInput,
			"connector s3: cannot list bucket (check bucket, prefix, region, and credentials)",
		)
	}
	return coreerr.Wrap(
		err,
		coreerr.CodeUnavailable,
		"connector s3: object store is unreachable",
	)
}
