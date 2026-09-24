package unit_test

import (
	"path/filepath"
	"testing"

	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/config"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/config/schema/infra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// baseWithDB writes a minimal base.yaml with a database + storage section into a
// temp dir and returns the dir, for the env-binding tests.
func baseWithDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
database:
  host: "localhost"
  port: 5432
  max_connections: 25
storage:
  s3:
    bucket: "documents"
`)
	return dir
}

// bindingSchema is a consumer root config: the loader derives an env binding for
// every leaf, under the consumer's prefix, from these mapstructure/envalias tags.
type bindingSchema struct {
	// Database is the database section (database.*).
	Database infra.DatabaseConfig `mapstructure:"database"`
	// Storage groups the object store under storage.s3.
	Storage struct {
		// S3 is the object-store section (storage.s3.*).
		S3 infra.S3Config `mapstructure:"s3"`
	} `mapstructure:"storage"`
	// Auth is the auth section (auth.*).
	Auth infra.AuthConfig `mapstructure:"auth"`
	// Redis is the cache section (redis.*).
	Redis infra.RedisConfig `mapstructure:"redis"`
	// Messaging groups the queue backend under messaging.sqs.
	Messaging struct {
		// SQS is the queue section (messaging.sqs.*).
		SQS infra.SQSConfig `mapstructure:"sqs"`
	} `mapstructure:"messaging"`
}

// loadWithSchema builds the loader over dir with bindingSchema and the MYAPP prefix,
// the way a consumer builds it.
func loadWithSchema(t *testing.T, dir string) interfaces.ConfigLoader {
	t.Helper()
	cfg, err := config.New(config.KindViper,
		config.WithBaseDir(dir),
		config.WithEnvironment(""),
		config.WithEnvPrefix("MYAPP"),
		config.WithSchema(&bindingSchema{}),
	)
	require.NoError(t, err)
	return cfg
}

// TestConfig_DerivedEnvBinding_UnboundNestedField tests that a nested config
// field with no hand-written BindEnv is overridable via its derived <PREFIX>_<PATH>
// env var through UnmarshalKey.
//
// Why this test is important:
//   - Env bindings are derived from the mapstructure tags of the consumer's schema,
//     so adding a field auto-binds its <PREFIX>_<PATH> var; without a binding,
//     UnmarshalKey silently ignores the env var for a key absent from the YAML.
//
// What it tests:
//   - MYAPP_DATABASE_MAX_CONNECTIONS overrides database.max_connections when the
//     "database" section is unmarshaled — with no loader edit for that field.
func TestConfig_DerivedEnvBinding_UnboundNestedField(t *testing.T) {
	dir := baseWithDB(t)
	t.Setenv("MYAPP_DATABASE_MAX_CONNECTIONS", "77")

	cfg := loadWithSchema(t, dir)

	var db infra.DatabaseConfig
	require.NoError(t, cfg.UnmarshalKey("database", &db))
	assert.Equal(
		t,
		77,
		db.MaxConnections,
		"MYAPP_DATABASE_MAX_CONNECTIONS must bind to database.max_connections via the derived tag path",
	)
}

// TestConfig_LegacyEnvAlias_StillResolves tests that the legacy flat aliases
// (DB_HOST, S3_BUCKET, from the envalias tags) override their mapped fields.
//
// Why this test is important:
//   - Deployments set the flat DB_HOST / S3_BUCKET names; a dropped alias silently
//     falls back to the YAML default (wrong DB host / bucket) — a production outage.
//
// What it tests:
//   - DB_HOST overrides database.host and S3_BUCKET overrides storage.s3.bucket
//     through UnmarshalKey.
func TestConfig_LegacyEnvAlias_StillResolves(t *testing.T) {
	dir := baseWithDB(t)
	t.Setenv("DB_HOST", "db.internal")
	t.Setenv("S3_BUCKET", "prod-bucket")

	cfg := loadWithSchema(t, dir)

	var db infra.DatabaseConfig
	require.NoError(t, cfg.UnmarshalKey("database", &db))
	assert.Equal(t, "db.internal", db.Host, "DB_HOST must still override database.host")

	var s3 infra.S3Config
	require.NoError(t, cfg.UnmarshalKey("storage.s3", &s3))
	assert.Equal(
		t,
		"prod-bucket",
		s3.Bucket,
		"S3_BUCKET must still override storage.s3.bucket",
	)
}

// TestConfig_CanonicalPrefixedPath_Resolves tests that the canonical
// <PREFIX>_<PATH> env var overrides its field (the primary, prefix-derived name).
//
// Why this test is important:
//   - <PREFIX>_<PATH> is the canonical override every field gets; a regression here
//     would break the documented, prefix-based override convention.
//
// What it tests:
//   - MYAPP_DATABASE_HOST overrides database.host through UnmarshalKey.
func TestConfig_CanonicalPrefixedPath_Resolves(t *testing.T) {
	dir := baseWithDB(t)
	t.Setenv("MYAPP_DATABASE_HOST", "canonical.host")

	cfg := loadWithSchema(t, dir)

	var db infra.DatabaseConfig
	require.NoError(t, cfg.UnmarshalKey("database", &db))
	assert.Equal(
		t,
		"canonical.host",
		db.Host,
		"MYAPP_DATABASE_HOST must override database.host",
	)
}

// TestConfig_ServiceTokens_FlatEnvPopulatesMap tests that a flat
// "caller=token;caller=token" env var (SERVICE_AUTH_TOKENS) populates the
// auth.service_tokens map[string]string through UnmarshalKey, preserving a
// caller's comma-separated {current,previous} rotation pair.
//
// Why this test is important:
//   - A deploy delivers the per-caller service tokens a
//     server accepts as a single Kubernetes secretKeyRef env var — a scalar, the
//     only shape a secretKeyRef can carry. Without a string→map decode hook the
//     map field stays nil, so in enforce mode every internal caller is rejected: a
//     fleet-wide internal-RPC outage. The hook is the foundation the server-side
//     enforcement rests on.
//   - Entries are ";"-separated, not ","-separated, so a caller's dual-token
//     rotation value ("cur,prev") survives — a "," delimiter would truncate the
//     window and lock out the caller mid-rotation.
//
// What it tests:
//   - SERVICE_AUTH_TOKENS="ingestion=icur,iprev;notification=ntok" decodes into
//     {"ingestion":"icur,iprev","notification":"ntok"} on AuthConfig.ServiceTokens.
func TestConfig_ServiceTokens_FlatEnvPopulatesMap(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
auth:
  stub: true
`)
	t.Setenv("SERVICE_AUTH_TOKENS", "ingestion=icur,iprev;notification=ntok")

	cfg := loadWithSchema(t, dir)

	var auth infra.AuthConfig
	require.NoError(t, cfg.UnmarshalKey("auth", &auth))
	assert.Equal(
		t,
		map[string]string{
			"ingestion":    "icur,iprev",
			"notification": "ntok",
		},
		auth.ServiceTokens,
		"SERVICE_AUTH_TOKENS must decode into the auth.service_tokens map, preserving the rotation-pair ','",
	)
}
