package interfaces

import "context"

// ConnectorSource is the connector-family read seam any connector type satisfies — an object store now
// (S3), a record system (Salesforce) or a file SaaS (GDrive/SharePoint) later. It is deliberately
// transport-agnostic and AWS-free so it lives in dependency-free core; the concrete S3/OAuth clients
// live in go/clients/connector/<type>. It exposes the two reads a connector needs: a bounded
// reachability probe (TestConnection, the "test" button) and the resumable, memory-bounded page-by-page
// listing the sync engine crawls (ListPage). It does NOT fetch or normalize object BODIES —
// that (parse + embed + ingest) is the Python (Ray) bulk pipeline downstream of the differ.
type ConnectorSource interface {
	// TestConnection probes the source with the connector's credentials and returns a bounded count
	// of reachable items (e.g. objects under an S3 bucket/prefix), or a coded error when the source
	// is unreachable or the credentials are rejected.
	TestConnection(ctx context.Context) (int, error)

	// ListPage streams one page of the source's objects starting AFTER continuationToken (empty = the
	// start of the listing) and returns the page plus the token to resume from ("" when the listing is
	// exhausted). It is the resumable, memory-bounded primitive the sync engine crawls a large source
	// with — holding one page at a time (never the full key list) and checkpointing the token so a
	// crashed sync resumes from the last committed page rather than restarting a million-object crawl.
	// A non-positive limit lets the backend apply its default page size. A failure is a coded error
	// (transient CodeUnavailable vs terminal CodeInvalidInput) so the caller can classify retry-vs-fail.
	ListPage(
		ctx context.Context,
		continuationToken string,
		limit int,
	) (objects []StorageObject, nextToken string, err error)
}

// ConnectorKind selects a connector backend (the family discriminator), mirroring the storage tier's
// Kind pattern. S3 now; gdrive/salesforce/… are added as new kinds — a config change, not an edit to
// the builder's callers or a sync engine.
type ConnectorKind string

// ConnectorKindS3 is the S3-compatible object-store connector (the first — and, for now, only — kind).
const ConnectorKindS3 ConnectorKind = "s3"

// AuthConfig is a connector's decrypted credentials — the plaintext form of the AES-GCM-sealed
// auth_config_enc column. Exactly one shape is populated, matching the connector's AuthMethod: the IAM
// role ARN (STS AssumeRole) or the static access key + secret. It is the SINGLE cross-service wire
// contract: the API service marshals + encrypts it on write, and the connector-sync workers decrypt +
// unmarshal into it — so the json tags live here once, replacing the API's domain struct and the
// worker's private mirror (which previously drifted apart behind a "keep in lockstep" comment). It lives
// in dependency-free core so both the API domain and the workers alias/import the one type without
// pulling the AWS-typed connector clients. Never log it; the secret is write-only on read paths.
type AuthConfig struct {
	// IAMRoleARN is the role assumed for the iam_role method.
	IAMRoleARN string `json:"iam_role_arn,omitempty"`
	// AccessKeyID is the access key id for the access_key method.
	AccessKeyID string `json:"access_key_id,omitempty"`
	// SecretAccessKey is the secret for the access_key method (write-only; never returned on a read path).
	SecretAccessKey string `json:"secret_access_key,omitempty"`
}

// SourceConfig is the primitive, domain-free, AWS-free description a SourceBuilder turns into a
// ConnectorSource. The app maps its domain.Connector + decrypted credentials onto this at the
// composition root, so pkg never imports app types. Its credential fields are already-decrypted
// plaintext (the store owns encryption-at-rest); never log them.
type SourceConfig struct {
	// Kind selects the connector backend.
	Kind ConnectorKind
	// Bucket names the source object-store bucket (S3 kind).
	Bucket string
	// Prefix restricts the listing to objects under this key prefix (S3 kind).
	Prefix string
	// Region is the connector's bucket region (S3 kind); empty falls back to the client default. The
	// object-store ENDPOINT is deliberately NOT here — it is app-infra (dev MinIO), captured by the
	// composition root's client builder, not a per-connector value.
	Region string
	// AuthMethod selects the credential strategy: "iam_role" (STS AssumeRole) or "access_key" (static).
	AuthMethod string
	// IAMRoleARN is the role assumed for auth_method=iam_role.
	IAMRoleARN string
	// AccessKeyID is the access key id for auth_method=access_key.
	AccessKeyID string
	// SecretAccessKey is the secret for auth_method=access_key (already-decrypted plaintext; never log it).
	SecretAccessKey string
}

// SourceBuilder builds a ConnectorSource from a SourceConfig, failing loudly on an unknown kind (the
// foundation/logger NewFromConfig pattern). It is the seam the connector service depends on and the
// composition root injects; the concrete builder lives in go/clients/connector.
type SourceBuilder interface {
	// Build constructs the ConnectorSource for cfg.Kind, or returns a coded error on an unknown kind
	// or a construction failure.
	Build(ctx context.Context, cfg SourceConfig) (ConnectorSource, error)
}
