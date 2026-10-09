// Package keyspaces builds the gocql cluster configuration for Amazon Keyspaces:
// the regional endpoint on port 9142, mandatory TLS with host verification, SigV4
// authentication (the AWS SigV4 gocql plugin, credentials from an AWS credentials
// provider — the default chain, IRSA in-cluster) and DisableInitialHostLookup, per
// AWS guidance. It never dials; the parent package creates the session.
package keyspaces

import (
	"context"
	"crypto/tls"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sigv4-auth-cassandra-gocql-driver-plugin/sigv4"
	"github.com/gocql/gocql"
	"github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// credentialsTimeout bounds one credentials refresh (an IRSA token exchange).
const credentialsTimeout = 5 * time.Second

// Config is the Amazon Keyspaces connection configuration.
type Config struct {
	// Credentials supplies SigV4 credentials; nil loads the AWS default chain.
	Credentials aws.CredentialsProvider
	// Region is the AWS region of the Keyspaces endpoint (required).
	Region string
	// Keyspace is the keyspace sessions use.
	Keyspace string
	// Consistency is the default consistency level name (LOCAL_QUORUM for Keyspaces).
	Consistency string
	// CACertPath, when set, is a PEM CA bundle to trust instead of the system pool.
	CACertPath string
	// Timeout bounds each request.
	Timeout time.Duration
	// ConnectTimeout bounds each connection attempt.
	ConnectTimeout time.Duration
	// Port is the TLS native-protocol port (9142).
	Port int
	// PageSize is the default page size of a query.
	PageSize int
	// NumConns is the number of connections per host.
	NumConns int
}

// Endpoint returns the Keyspaces service endpoint host for region.
func Endpoint(region string) string {
	return "cassandra." + region + ".amazonaws.com"
}

// New returns the cluster configuration for cfg, without dialing. A missing
// region or an unknown consistency is CodeInvalidInput; a default credentials
// chain that cannot be loaded is CodeInternal.
func New(cfg *Config) (*gocql.ClusterConfig, error) {
	if cfg.Region == "" {
		return nil, errors.New(errors.CodeInvalidInput, "keyspaces: region is required")
	}
	consistency, err := gocql.ParseConsistencyWrapper(cfg.Consistency)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInvalidInput, "keyspaces: unknown consistency "+cfg.Consistency)
	}
	provider := cfg.Credentials
	if provider == nil {
		awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(cfg.Region))
		if err != nil {
			return nil, errors.Wrap(err, errors.CodeInternal, "keyspaces: load AWS credentials chain")
		}
		provider = awsCfg.Credentials
	}
	host := Endpoint(cfg.Region)
	cluster := gocql.NewCluster(host)
	cluster.Port = cfg.Port
	cluster.Keyspace = cfg.Keyspace
	cluster.Consistency = consistency
	cluster.Timeout = cfg.Timeout
	cluster.ConnectTimeout = cfg.ConnectTimeout
	cluster.PageSize = cfg.PageSize
	if cfg.NumConns > 0 {
		cluster.NumConns = cfg.NumConns
	}
	cluster.DisableInitialHostLookup = true
	cluster.SslOpts = &gocql.SslOptions{
		Config:                 &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12},
		CaPath:                 cfg.CACertPath,
		EnableHostVerification: true,
	}
	cluster.Authenticator = sigv4.NewAwsAuthenticatorWithCredentialCallback(cfg.Region, credentialsCallback(provider))
	return cluster, nil
}

// credentialsCallback adapts an AWS SDK v2 credentials provider to the SigV4
// plugin's callback, bounding each refresh.
func credentialsCallback(provider aws.CredentialsProvider) sigv4.SigV4CredentialsCallback {
	return func() (sigv4.SigV4Credentials, error) {
		ctx, cancel := context.WithTimeout(context.Background(), credentialsTimeout)
		defer cancel()
		creds, err := provider.Retrieve(ctx)
		if err != nil {
			return sigv4.SigV4Credentials{}, errors.Wrap(err, errors.CodeUnavailable, "keyspaces: retrieve AWS credentials")
		}
		return sigv4.SigV4Credentials{
			AccessKeyId: creds.AccessKeyID, SecretAccessKey: creds.SecretAccessKey, SessionToken: creds.SessionToken,
		}, nil
	}
}
