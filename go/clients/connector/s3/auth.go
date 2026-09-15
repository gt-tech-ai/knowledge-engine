// Package s3 implements the S3 ConnectorSource for the connector family: a "test connection" probe that
// lists a bounded page under a connector's bucket/prefix using the connector's OWN credentials, over the
// shared StorageClient (INJECTED — this package never imports pkg/go/clients/storage, so there is no
// lateral same-layer import). Its credential strategies (STS AssumeRole, static access key) are the
// S3-specific, AWS-typed part of the family; an OAuth2-with-refresh strategy for SaaS connectors is a
// future sibling package.
package s3

import (
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"

	coreerr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// Auth method vocabulary (mirrors the connector's stored auth_method values).
const (
	// authMethodIAMRole assumes the connector's role via STS for credentials.
	authMethodIAMRole = "iam_role"
	// authMethodAccessKey uses a static access key/secret pair.
	authMethodAccessKey = "access_key"
)

// credentialsFromConfig selects the S3 credential strategy for method, failing loudly on an unknown
// method (the foundation/logger NewFromConfig pattern): access_key presents the static key; iam_role
// assumes the connector's role via STS (lazy — the provider retrieves on first use). The STS client is
// used only by iam_role.
func credentialsFromConfig(
	method, iamRoleARN, accessKeyID, secretAccessKey string,
	stsClient stscreds.AssumeRoleAPIClient,
) (aws.CredentialsProvider, error) {
	switch method {
	case authMethodAccessKey:
		return credentials.NewStaticCredentialsProvider(
			accessKeyID,
			secretAccessKey,
			"",
		), nil
	case authMethodIAMRole:
		return stscreds.NewAssumeRoleProvider(stsClient, iamRoleARN), nil
	default:
		return nil, coreerr.New(
			coreerr.CodeInvalidInput,
			fmt.Sprintf("connector s3 auth: unknown method %q", method),
		)
	}
}
