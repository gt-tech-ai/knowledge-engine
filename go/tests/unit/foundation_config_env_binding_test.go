package unit_test

import (
	"path/filepath"
	"testing"

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

// TestConfig_DerivedEnvBinding_UnboundNestedField tests that a nested config
// field with no hand-written BindEnv is overridable via its derived SEARCH_<PATH>
// env var through UnmarshalKey.
//
// Why this test is important:
//   - This is the whole point of: env bindings are derived from the
//     mapstructure tag, so adding a field auto-binds its SEARCH_<PATH> var. Before
//     tag-derivation, database.max_connections had no BindEnv, so
//     SEARCH_DATABASE_MAX_CONNECTIONS was silently ignored by UnmarshalKey.
//
// What it tests:
//   - SEARCH_DATABASE_MAX_CONNECTIONS overrides database.max_connections when the
//     "database" section is unmarshaled — with no loader edit for that field.
func TestConfig_DerivedEnvBinding_UnboundNestedField(t *testing.T) {
	dir := baseWithDB(t)
	t.Setenv("SEARCH_DATABASE_MAX_CONNECTIONS", "77")

	cfg, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

	var db infra.DatabaseConfig
	require.NoError(t, cfg.UnmarshalKey("database", &db))
	assert.Equal(
		t,
		77,
		db.MaxConnections,
		"SEARCH_DATABASE_MAX_CONNECTIONS must bind to database.max_connections via the derived tag path",
	)
}

// TestConfig_LegacyEnvAlias_StillResolves tests that the legacy flat aliases
// (DB_HOST, S3_BUCKET) still override their mapped fields after the binding is
// derived from tags rather than the hand-maintained bindLegacyEnvVars.
//
// Why this test is important:
//   - Deployments set the flat DB_HOST / S3_BUCKET names; if the tag-derived
//     rewrite dropped an alias, the deployed service would silently fall back to
//     the YAML default (wrong DB host / bucket) — a production outage.
//
// What it tests:
//   - DB_HOST overrides database.host and S3_BUCKET overrides storage.s3.bucket
//     through UnmarshalKey.
func TestConfig_LegacyEnvAlias_StillResolves(t *testing.T) {
	dir := baseWithDB(t)
	t.Setenv("DB_HOST", "db.internal")
	t.Setenv("S3_BUCKET", "prod-bucket")

	cfg, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

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

// TestConfig_CanonicalSearchPath_Resolves tests that the canonical
// SEARCH_<PATH> env var overrides its field (the primary, prefix-derived name).
//
// Why this test is important:
//   - SEARCH_<PATH> is the canonical override every field gets; a regression here
//     would break the documented, prefix-based override convention.
//
// What it tests:
//   - SEARCH_DATABASE_HOST overrides database.host through UnmarshalKey.
func TestConfig_CanonicalSearchPath_Resolves(t *testing.T) {
	dir := baseWithDB(t)
	t.Setenv("SEARCH_DATABASE_HOST", "canonical.host")

	cfg, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

	var db infra.DatabaseConfig
	require.NoError(t, cfg.UnmarshalKey("database", &db))
	assert.Equal(
		t,
		"canonical.host",
		db.Host,
		"SEARCH_DATABASE_HOST must override database.host",
	)
}

// TestConfig_ServiceTokens_FlatEnvPopulatesMap tests that a flat
// "caller=token;caller=token" env var (SERVICE_AUTH_TOKENS) populates the
// auth.service_tokens map[string]string through UnmarshalKey, preserving a
// caller's comma-separated {current,previous} rotation pair.
//
// Why this test is important:
//   - The deploy path (Phase 1) delivers the per-caller service tokens a
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

	cfg, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

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

// TestConfig_NonSchemaKeys_StillBind tests that the keys read via GetString
// rather than unmarshaled (not schema struct fields) still resolve their env
// vars after the tag-derivation refactor moved the rest into deriveEnvBindings.
//
// Why this test is important:
//   - messaging.sqs.kind (backend selector), auth.auth0_client_id (identity M2M
//     grant), and server.api.identity_target (api→identity dial) are read via
//     GetString and set from env/secrets in deployments — not YAML. If the
//     refactor dropped these explicit binds, the deployed services would fail
//     startup or silently mis-route. This guards the bindNonSchemaEnv remnant.
//
// What it tests:
//   - SQS_KIND, AUTH0_CLIENT_ID, and IDENTITY_GRPC_TARGET override their keys.
func TestConfig_NonSchemaKeys_StillBind(t *testing.T) {
	dir := baseWithDB(t)
	t.Setenv("SQS_KIND", "sqs")
	t.Setenv("AUTH0_CLIENT_ID", "cid-123")
	t.Setenv("IDENTITY_GRPC_TARGET", "identity:8090")

	cfg, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

	assert.Equal(
		t,
		"sqs",
		cfg.GetString("messaging.sqs.kind"),
		"SQS_KIND must bind messaging.sqs.kind",
	)
	assert.Equal(
		t,
		"cid-123",
		cfg.GetString("auth.auth0_client_id"),
		"AUTH0_CLIENT_ID must bind auth.auth0_client_id",
	)
	assert.Equal(t, "identity:8090", cfg.GetString("server.api.identity_target"),
		"IDENTITY_GRPC_TARGET must bind server.api.identity_target")
}

// TestConfig_SeedPersonaPassword_FlatEnvBinds tests that the unprefixed
// SEED_PERSONA_PASSWORD env var overrides seed.persona_password (read via
// GetString), the seed personas Job's initial-password contract.
//
// Why this test is important:
//   - seed.persona_password has no committed value in a deployed tenant (base.yaml
//     ships no default); staging injects it as the SEED_PERSONA_PASSWORD env var
//     from ESO (persona-credentials), the SAME AWS-SM value the edge Job logs in
//     with. AutomaticEnv applies the SEARCH_ prefix, so without an explicit BindEnv
//     the unprefixed SEED_PERSONA_PASSWORD is silently ignored and the seed Job
//     falls back to the committed dev-only default — leaking it into staging and
//     drifting from the edge login (login failure).
//
// What it tests:
//   - SEED_PERSONA_PASSWORD overrides seed.persona_password through GetString.
func TestConfig_SeedPersonaPassword_FlatEnvBinds(t *testing.T) {
	dir := baseWithDB(t)
	t.Setenv("SEED_PERSONA_PASSWORD", "Injected-Pw123!")

	cfg, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

	assert.Equal(
		t,
		"Injected-Pw123!",
		cfg.GetString("seed.persona_password"),
		"SEED_PERSONA_PASSWORD must bind seed.persona_password",
	)
}
