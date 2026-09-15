package infra

// SQSConfig holds SQS-compatible messaging configuration.
type SQSConfig struct {
	// Queues maps logical queue names to their SQS queue names (e.g. "document_upload" -> "document-upload").
	Queues map[string]string `mapstructure:"queues"`

	// Endpoint is the SQS-compatible API endpoint URL (e.g. "http://localhost:9324" for ElasticMQ).
	Endpoint string `mapstructure:"endpoint" envalias:"SQS_ENDPOINT"`

	// Region is the AWS region where the SQS queues are provisioned (e.g. "us-east-1").
	Region string `mapstructure:"region" envalias:"SQS_REGION"`

	// AccessKeyID is the static access key for local development (ElasticMQ). Empty
	// falls back to the default AWS credential chain (env, IAM role / IRSA).
	AccessKeyID string `mapstructure:"access_key_id" envalias:"SQS_ACCESS_KEY_ID"`

	// SecretAccessKey is the static secret paired with AccessKeyID. Ignored when
	// AccessKeyID is empty.
	SecretAccessKey string `mapstructure:"secret_access_key" envalias:"SQS_SECRET_ACCESS_KEY"`
}

// DefaultSQSConfig returns an SQSConfig with defaults for local ElasticMQ.
func DefaultSQSConfig() SQSConfig {
	return SQSConfig{
		Queues: map[string]string{
			"document_upload": "document-upload",
			"document_status": "document-status",
			"notification":    "notification",
		},
		Endpoint:        "http://localhost:9324",
		Region:          "us-east-1",
		AccessKeyID:     "local",
		SecretAccessKey: "local",
	}
}

// Validate returns an error if the configuration is invalid.
func (c SQSConfig) Validate() error {
	return nil
}
