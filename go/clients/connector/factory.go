// Package connector builds ConnectorSources for the connector family (→ pkg): S3 now, a
// future GDrive/Salesforce kind added as a new case + sibling package (charter One Idea; the
// foundation/logger NewFromConfig pattern). It imports only the storage INTERFACE — via the injected
// S3ClientBuilder — never pkg/go/clients/storage, so there is no lateral same-layer import.
package connector

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"

	connectordecorators "github.com/gt-tech-ai/knowledge-engine/go/clients/connector/decorators"
	connectors3 "github.com/gt-tech-ai/knowledge-engine/go/clients/connector/s3"
	coreerr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
)

// builder is the connector SourceBuilder: it turns a SourceConfig into a decorated ConnectorSource,
// dispatching on Kind and failing loud on an unknown kind.
type builder struct {
	// buildS3Client constructs a connector-scoped StorageClient; injected by the app root so this
	// package never imports pkg/go/clients/storage.
	buildS3Client connectors3.S3ClientBuilder
	// sts is the AssumeRole client used only by the iam_role strategy.
	sts stscreds.AssumeRoleAPIClient
	// obs are the observability collaborators wrapped around each source.
	obs connectordecorators.Deps
}

// Ensure builder satisfies the SourceBuilder contract.
var _ interfaces.SourceBuilder = (*builder)(nil)

// NewSourceBuilder builds the SourceBuilder over the injected S3-client builder (supplied by the app
// composition root so pkg/clients/connector never imports pkg/clients/storage), the STS client (used
// only by the iam_role strategy), and the observability deps for the decorator.
func NewSourceBuilder(
	buildS3Client connectors3.S3ClientBuilder,
	sts stscreds.AssumeRoleAPIClient,
	obs connectordecorators.Deps,
) interfaces.SourceBuilder {
	return &builder{buildS3Client: buildS3Client, sts: sts, obs: obs}
}

// Build constructs the decorated ConnectorSource for cfg.Kind, failing loud (CodeInvalidInput) on an
// unknown kind.
func (b *builder) Build(
	ctx context.Context,
	cfg interfaces.SourceConfig,
) (interfaces.ConnectorSource, error) {
	switch cfg.Kind {
	case interfaces.ConnectorKindS3:
		src, err := connectors3.NewSource(ctx, cfg, b.sts, b.buildS3Client)
		if err != nil {
			return nil, err
		}
		return connectordecorators.Wrap(src, "connector-s3", b.obs), nil
	default:
		return nil, coreerr.New(
			coreerr.CodeInvalidInput,
			fmt.Sprintf("connector: unknown kind %q", cfg.Kind),
		)
	}
}
