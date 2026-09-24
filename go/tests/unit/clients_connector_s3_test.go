package unit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	connector "github.com/gt-tech-ai/knowledge-engine/go/clients/connector"
	connectordecorators "github.com/gt-tech-ai/knowledge-engine/go/clients/connector/decorators"
	connectors3 "github.com/gt-tech-ai/knowledge-engine/go/clients/connector/s3"
	coreerr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/mocks"
)

// stubBuilder returns an S3ClientBuilder that hands back sc and (optionally) captures the creds it was
// given, so a test can assert the auth strategy actually produced a credentials provider.
func stubBuilder(
	sc interfaces.StorageClient,
	captured *aws.CredentialsProvider,
) connectors3.S3ClientBuilder {
	return func(
		_ context.Context,
		creds aws.CredentialsProvider,
		_ string,
	) (interfaces.StorageClient, error) {
		if captured != nil {
			*captured = creds
		}
		return sc, nil
	}
}

func s3Cfg(method string) interfaces.SourceConfig {
	return interfaces.SourceConfig{
		Kind:            interfaces.ConnectorKindS3,
		Bucket:          "b",
		Prefix:          "docs/",
		Region:          "us-east-1",
		AuthMethod:      method,
		AccessKeyID:     "AK",
		SecretAccessKey: "SK",
		IAMRoleARN:      "arn:aws:iam::1:role/r",
		ExternalID:      "ext-1",
	}
}

// TestS3ConnectorSource_TestConnection tests the S3 ConnectorSource probe over a mocked StorageClient:
// it counts objects under the bucket/prefix and classifies failures.
//
// Why this test is important:
//   - TestConnection is the connector "test" button; it must reuse the shared StorageClient (bounded
//     ListObjectsPage) and return a TRUTHFUL coded error — a bad bucket/credentials (client fault) is the
//     user's problem (InvalidInput), a transport/service failure is transient (Unavailable) — so the UI
//     shows the right message and retriability.
//
// What it tests:
//   - a successful bounded list returns the object count; a client-fault AWS API error → CodeInvalidInput;
//     a transport (non-API) error → CodeUnavailable.
func TestS3ConnectorSource_TestConnection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("counts objects under the prefix", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		sc := mocks.NewMockStorageClient(ctrl)
		sc.EXPECT().ListObjectsPage(gomock.Any(), "b", "docs/", gomock.Any()).
			Return([]interfaces.StorageObject{{Key: "docs/a"}, {Key: "docs/b"}}, nil)

		src, err := connectors3.NewSource(
			ctx,
			s3Cfg("access_key"),
			nil,
			stubBuilder(sc, nil),
		)
		require.NoError(t, err)
		n, err := src.TestConnection(ctx)
		require.NoError(t, err)
		require.Equal(t, 2, n)
	})

	t.Run("a client-fault API error → InvalidInput", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		sc := mocks.NewMockStorageClient(ctrl)
		sc.EXPECT().
			ListObjectsPage(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, &smithy.GenericAPIError{Code: "AccessDenied", Fault: smithy.FaultClient})

		src, _ := connectors3.NewSource(
			ctx,
			s3Cfg("access_key"),
			nil,
			stubBuilder(sc, nil),
		)
		_, err := src.TestConnection(ctx)
		require.True(
			t,
			coreerr.Is(err, coreerr.CodeInvalidInput),
			"bad bucket/creds is the user's problem",
		)
	})

	t.Run("a transport error → Unavailable", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		sc := mocks.NewMockStorageClient(ctrl)
		sc.EXPECT().
			ListObjectsPage(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, errors.New("dial tcp: connection refused"))

		src, _ := connectors3.NewSource(
			ctx,
			s3Cfg("access_key"),
			nil,
			stubBuilder(sc, nil),
		)
		_, err := src.TestConnection(ctx)
		require.True(
			t,
			coreerr.Is(err, coreerr.CodeUnavailable),
			"a transport failure is transient",
		)
	})
}

// TestS3ConnectorSource_ListPage tests the streaming crawl the sync engine pages a large bucket with:
// it threads the caller's continuation token through, surfaces the resume token, and classifies a
// failure (transient vs terminal) so the worker can decide retry-vs-fail.
//
// Why this test is important:
//   - The engine crawls a million-object bucket through this seam and checkpoints the returned
//     token to crash-resume; if the token weren't passed through the crawl would re-list from the start
//     (infinite loop / double work), and a mis-classified error would either retry a permanent failure
//     forever or give up on a transient one mid-crawl.
//
// What it tests:
//   - the caller's token reaches the StorageClient and the resume token is returned; a client-fault AWS
//     error → CodeInvalidInput (terminal); a transport error → CodeUnavailable (transient).
func TestS3ConnectorSource_ListPage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("streams a page and returns the resume token", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		sc := mocks.NewMockStorageClient(ctrl)
		sc.EXPECT().
			ListObjectsPageToken(gomock.Any(), "b", "docs/", "tok1", 1000).
			Return([]interfaces.StorageObject{{Key: "docs/a"}}, "tok2", nil)

		src, err := connectors3.NewSource(
			ctx,
			s3Cfg("access_key"),
			nil,
			stubBuilder(sc, nil),
		)
		require.NoError(t, err)
		objs, next, err := src.ListPage(ctx, "tok1", 1000)
		require.NoError(t, err)
		require.Len(t, objs, 1)
		require.Equal(t, "tok2", next, "the resume token is surfaced for checkpointing")
	})

	t.Run("a client-fault API error → InvalidInput (terminal)", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		sc := mocks.NewMockStorageClient(ctrl)
		sc.EXPECT().
			ListObjectsPageToken(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, "", &smithy.GenericAPIError{Code: "NoSuchBucket", Fault: smithy.FaultClient})

		src, _ := connectors3.NewSource(
			ctx,
			s3Cfg("access_key"),
			nil,
			stubBuilder(sc, nil),
		)
		_, _, err := src.ListPage(ctx, "", 1000)
		require.True(t, coreerr.Is(err, coreerr.CodeInvalidInput))
	})

	t.Run("a transport error → Unavailable (transient)", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		sc := mocks.NewMockStorageClient(ctrl)
		sc.EXPECT().
			ListObjectsPageToken(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, "", errors.New("dial tcp: connection refused"))

		src, _ := connectors3.NewSource(
			ctx,
			s3Cfg("access_key"),
			nil,
			stubBuilder(sc, nil),
		)
		_, _, err := src.ListPage(ctx, "", 1000)
		require.True(t, coreerr.Is(err, coreerr.CodeUnavailable))
	})
}

// TestS3ConnectorSource_AuthSelection tests the credential-strategy selection: an unknown method fails
// loud, and each known method yields a credentials provider handed to the client builder.
//
// Why this test is important:
//   - The auth strategy is the S3-specific seam (STS AssumeRole vs static key); a wrong or silent
//     selection would sign requests with the wrong (or no) credentials, and an unknown method must fail
//     at build time, not as a cryptic S3 error later.
//
// What it tests:
//   - an unknown auth_method → CodeInvalidInput at NewSource; access_key and iam_role each produce a
//     non-nil credentials provider passed to the injected builder.
func TestS3ConnectorSource_AuthSelection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	sc := mocks.NewMockStorageClient(ctrl)

	_, err := connectors3.NewSource(ctx, s3Cfg("bogus"), nil, stubBuilder(sc, nil))
	require.True(
		t,
		coreerr.Is(err, coreerr.CodeInvalidInput),
		"unknown auth method fails loud",
	)

	for _, method := range []string{"access_key", "iam_role"} {
		var captured aws.CredentialsProvider
		_, err := connectors3.NewSource(
			ctx,
			s3Cfg(method),
			mocks.NewMockAssumeRoleAPIClient(ctrl),
			stubBuilder(sc, &captured),
		)
		require.NoError(t, err, method)
		require.NotNil(
			t,
			captured,
			"the %s strategy yields a credentials provider",
			method,
		)
	}
}

// TestConnectorSourceBuilder_Build tests the factory: an unknown kind fails loud; the s3 kind builds a
// decorated source that probes through the injected StorageClient.
//
// Why this test is important:
//   - The builder is the connector family's config-selected entry point; an unknown kind must fail at
//     build time (CodeInvalidInput), and the s3 kind must produce a working ConnectorSource wired to the
//     injected client builder + observability decorator — so adding a kind is a config change, not an edit.
//
// What it tests:
//   - Build(kind="bogus") → CodeInvalidInput; Build(s3) → a source whose TestConnection lists via the
//     injected (mock) StorageClient and returns the count (the decorator forwards with nil-obs deps).
func TestConnectorSourceBuilder_Build(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("unknown kind fails loud", func(t *testing.T) {
		b := connector.NewSourceBuilder(nil, nil, connectordecorators.Deps{})
		_, err := b.Build(ctx, interfaces.SourceConfig{Kind: "bogus"})
		require.True(t, coreerr.Is(err, coreerr.CodeInvalidInput))
	})

	t.Run(
		"s3 kind builds a decorated source that probes via StorageClient",
		func(t *testing.T) {
			ctrl := gomock.NewController(t)
			sc := mocks.NewMockStorageClient(ctrl)
			sc.EXPECT().ListObjectsPage(gomock.Any(), "b", "docs/", gomock.Any()).
				Return([]interfaces.StorageObject{{Key: "docs/a"}}, nil)

			b := connector.NewSourceBuilder(
				stubBuilder(sc, nil),
				mocks.NewMockAssumeRoleAPIClient(ctrl),
				connectordecorators.Deps{},
			)
			src, err := b.Build(ctx, s3Cfg("access_key"))
			require.NoError(t, err)
			n, err := src.TestConnection(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, n)
		},
	)
}

// TestS3ConnectorSource_IAMRoleRequiresExternalID tests that the iam_role strategy always assumes the
// connector's role with the connector's ExternalId, and refuses to build without one.
//
// Why this test is important:
//   - Without an ExternalId, a tenant who registers another tenant's role ARN gets the platform to
//     assume it for them (the confused-deputy problem); the ExternalId in the role's trust policy is
//     what ties the role to the tenant that owns it.
//
// What it tests:
//   - Retrieving the iam_role credentials calls STS AssumeRole with the connector's RoleArn and
//     ExternalId.
//   - An iam_role config without an ExternalId fails at NewSource with CodeInvalidInput, and STS is
//     never called.
func TestS3ConnectorSource_IAMRoleRequiresExternalID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	sc := mocks.NewMockStorageClient(ctrl)
	stsClient := mocks.NewMockAssumeRoleAPIClient(ctrl)

	stsClient.EXPECT().
		AssumeRole(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context, in *sts.AssumeRoleInput, _ ...func(*sts.Options),
		) (*sts.AssumeRoleOutput, error) {
			require.Equal(t, "arn:aws:iam::1:role/r", aws.ToString(in.RoleArn))
			require.Equal(t, "ext-1", aws.ToString(in.ExternalId))
			return &sts.AssumeRoleOutput{Credentials: &ststypes.Credentials{
				AccessKeyId:     aws.String("ASIA"),
				SecretAccessKey: aws.String("secret"),
				SessionToken:    aws.String("token"),
				Expiration:      aws.Time(time.Now().Add(time.Hour)),
			}}, nil
		})

	var captured aws.CredentialsProvider
	_, err := connectors3.NewSource(ctx, s3Cfg("iam_role"), stsClient, stubBuilder(sc, &captured))
	require.NoError(t, err)
	creds, err := captured.Retrieve(ctx)
	require.NoError(t, err)
	require.Equal(t, "ASIA", creds.AccessKeyID)

	noExternalID := s3Cfg("iam_role")
	noExternalID.ExternalID = ""
	_, err = connectors3.NewSource(ctx, noExternalID, mocks.NewMockAssumeRoleAPIClient(ctrl), stubBuilder(sc, nil))
	require.True(t, coreerr.Is(err, coreerr.CodeInvalidInput), "iam_role without an external id fails loud: %v", err)
}
