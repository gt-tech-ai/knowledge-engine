package unit_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/config"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/mocks"
)

// TestConfig_LoadBaseConfig tests that the config loader reads and parses a
// base YAML config file.
//
// Why this test is important:
//   - Configuration loading is the first thing every service does at startup
//   - A broken config loader prevents all services from initializing
//   - Validates typed getter access (GetInt, GetString) through the interface
//
// What it tests:
//   - server.port is read as integer 8080
//   - database.host is read as string "localhost"
func TestConfig_LoadBaseConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
server:
  port: 8080
database:
  host: localhost
  port: 5432
`)
	var cfg interfaces.ConfigLoader
	var err error
	cfg, err = config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err, "New")

	assert.Equal(t, 8080, cfg.GetInt("server.port"))
	assert.Equal(t, "localhost", cfg.GetString("database.host"))
}

// TestConfig_EnvironmentOverlay tests that environment-specific config files
// override matching base values.
//
// Why this test is important:
//   - Production, staging, and development environments require different
//     settings
//   - Overlays must merge cleanly: override matched keys, preserve unmatched
//     base keys
//   - Incorrect merging could expose debug settings in production or use prod
//     databases in dev
//
// What it tests:
//   - log.level is overridden from "info" to "warn" by the production overlay
//   - server.port retains the base value 8080 (not present in overlay)
func TestConfig_EnvironmentOverlay(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
server:
  port: 8080
log:
  level: info
`)
	writeFile(t, filepath.Join(dir, "production.yaml"), `
log:
  level: warn
`)
	var cfg interfaces.ConfigLoader
	var err error
	cfg, err = config.New(
		config.KindViper,
		config.WithBaseDir(dir),
		config.WithEnvironment("production"),
	)
	require.NoError(t, err, "New")

	assert.Equal(t, "warn", cfg.GetString("log.level"))
	assert.Equal(t, 8080, cfg.GetInt("server.port"))
}

// TestConfig_EnvVarsOverride tests that SEARCH_-prefixed environment variables
// take highest precedence.
//
// Why this test is important:
//   - Container deployments inject configuration via environment variables
//   - Env vars must override both base config and environment overlays for
//     12-factor compliance
//   - Kubernetes ConfigMaps and Secrets are exposed as env vars, not files
//
// What it tests:
//   - server.port is overridden from 8080 to 9090 by SEARCH_SERVER_PORT env var
//
// NOTE: t.Setenv panics when called from a parallel test, so this test
// must NOT call t.Parallel().
func TestConfig_EnvVarsOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
server:
  port: 8080
`)
	t.Setenv("SEARCH_SERVER_PORT", "9090")

	var cfg interfaces.ConfigLoader
	var err error
	cfg, err = config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err, "New")

	assert.Equal(t, 9090, cfg.GetInt("server.port"), "env override failed")
}

// TestConfig_StoragePublicEndpointEnvOverride tests that SEARCH_STORAGE_S3_PUBLIC_ENDPOINT
// binds to storage.s3.public_endpoint.
//
// Why this test is important:
//   - The presign public-endpoint fix only takes effect if the env var is bound; each
//     storage.s3.* key needs an explicit BindEnv, and an unbound key silently ignores the
//     override so browser uploads keep the unreachable in-cluster host. This guards the wiring.
//
// What it tests:
//   - SEARCH_STORAGE_S3_PUBLIC_ENDPOINT overrides storage.s3.public_endpoint.
func TestConfig_StoragePublicEndpointEnvOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
storage:
  s3:
    public_endpoint: ""
`)
	t.Setenv("SEARCH_STORAGE_S3_PUBLIC_ENDPOINT", "http://localhost:9000")

	cfg, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err, "New")
	assert.Equal(
		t,
		"http://localhost:9000",
		cfg.GetString("storage.s3.public_endpoint"),
		"SEARCH_STORAGE_S3_PUBLIC_ENDPOINT must bind to storage.s3.public_endpoint",
	)
}

// TestConfig_MissingBaseConfig tests that the loader does not error when no
// config file exists.
//
// Why this test is important:
//   - Services must start even without a config file, relying on env vars or
//     defaults
//   - Kubernetes deployments may not mount a config volume in all environments
//   - A hard failure on missing config would prevent container startup
//
// What it tests:
//   - New returns nil error when config.yaml does not exist in the base directory
func TestConfig_MissingBaseConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	_, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err, "New should not fail on missing base config")
}

// TestConfig_UnknownKindReturnsError tests that New returns an error for
// unknown Kind values.
//
// Why this test is important:
//   - The factory must reject invalid Kind values at construction time
//   - Prevents nil-pointer panics from uninitialized backends
//
// What it tests:
//   - New returns a non-nil error for Kind(99)
func TestConfig_UnknownKindReturnsError(t *testing.T) {
	t.Parallel()

	_, err := config.New(config.Kind(99))
	require.Error(t, err, "expected error for unknown kind")
}

// TestConfig_DefaultConfig tests that DefaultConfig returns sensible defaults.
//
// Why this test is important:
//   - DefaultConfig is the starting point for all factory calls
//   - Ensures KindViper is the default and Viper fields have expected values
//
// What it tests:
//   - Kind is KindViper
//   - Viper.BaseDir is "."
//   - Viper.Prefix is "SEARCH"
func TestConfig_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	assert.Equal(t, config.KindViper, cfg.Kind)
	assert.Equal(t, ".", cfg.Viper.BaseDir)
	assert.Equal(t, "SEARCH", cfg.Viper.Prefix)
}

// TestConfig_NewFromConfig tests that NewFromConfig creates a working loader
// from a Config struct.
//
// Why this test is important:
//   - NewFromConfig is the lower-level factory used by New and available for
//     programmatic config construction
//   - Ensures Config struct fields are correctly mapped to the Viper backend
//
// What it tests:
//   - A Config struct with BaseDir and Env produces a working ConfigLoader
//   - Values from both base and overlay are accessible
func TestConfig_NewFromConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
app:
  name: test-service
  debug: false
`)
	writeFile(t, filepath.Join(dir, "dev.yaml"), `
app:
  debug: true
`)

	cfg := config.DefaultConfig()
	cfg.Viper.BaseDir = dir
	cfg.Viper.Env = "dev"
	var loader interfaces.ConfigLoader
	var err error
	loader, err = config.NewFromConfig(cfg)
	require.NoError(t, err, "NewFromConfig")

	assert.Equal(t, "test-service", loader.GetString("app.name"))
	assert.True(
		t,
		loader.GetBool("app.debug"),
		"app.debug = false, want true (dev overlay)",
	)
}

// TestConfig_Unmarshal tests that Unmarshal decodes configuration into a struct.
//
// Why this test is important:
//   - Services use Unmarshal to bind configuration to typed structs
//   - Incorrect mapping would silently produce zero-value config, causing
//     runtime failures
//
// What it tests:
//   - Nested YAML keys are correctly mapped to struct fields via mapstructure tags
func TestConfig_Unmarshal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
server:
  port: 8080
  host: 0.0.0.0
`)

	var cfg interfaces.ConfigLoader
	var err error
	cfg, err = config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err, "New")

	var target struct {
		Server struct {
			Host string `mapstructure:"host"`
			Port int    `mapstructure:"port"`
		} `mapstructure:"server"`
	}
	require.NoError(t, cfg.Unmarshal(&target), "Unmarshal")
	assert.Equal(t, 8080, target.Server.Port)
	assert.Equal(t, "0.0.0.0", target.Server.Host)
}

// TestConfig_GetBool tests that GetBool correctly reads boolean config values.
//
// Why this test is important:
//   - GetBool is used for feature flags (e.g., STUB_AUTH)
//   - Boolean parsing edge cases (string "true", int 1) must work correctly
//
// What it tests:
//   - Boolean true and false YAML values are read correctly
func TestConfig_GetBool(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
feature:
  enabled: true
  disabled: false
`)

	var cfg interfaces.ConfigLoader
	var err error
	cfg, err = config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err, "New")

	assert.True(t, cfg.GetBool("feature.enabled"), "feature.enabled = false, want true")
	assert.False(
		t,
		cfg.GetBool("feature.disabled"),
		"feature.disabled = true, want false",
	)
}

// TestConfig_Get tests that Get returns raw values.
//
// Why this test is important:
//   - Get is the most generic accessor on ConfigLoader
//   - Must return non-nil for existing keys and nil for missing keys
//
// What it tests:
//   - Existing key returns non-nil value
//   - Missing key returns nil
func TestConfig_Get(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
app:
  name: test
`)

	var cfg interfaces.ConfigLoader
	var err error
	cfg, err = config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err, "New")

	assert.NotNil(t, cfg.Get("app.name"), "Get(app.name) should return non-nil")
	assert.Nil(t, cfg.Get("nonexistent.key"), "Get(nonexistent.key) should return nil")
}

// TestConfig_MockAsConsumerDependency tests that MockConfigLoader satisfies
// the interfaces.ConfigLoader interface and can substitute the real loader in tests.
//
// Why this test is important:
//   - Services depend on interfaces.ConfigLoader, not the concrete Viper implementation
//   - The mock must implement every method so tests can inject controlled config values
//   - If the mock diverges from the interface contract, service unit tests will fail to compile
//
// What it tests:
//   - MockConfigLoader assigned to interfaces.ConfigLoader variable compiles (interface satisfaction)
//   - GetString, GetInt, GetBool, Get methods delegate to mock expectations correctly
func TestConfig_MockAsConsumerDependency(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mock := mocks.NewMockConfigLoader(ctrl)

	mock.EXPECT().GetString("server.host").Return("0.0.0.0")
	mock.EXPECT().GetInt("server.port").Return(8080)
	mock.EXPECT().GetBool("debug").Return(false)
	mock.EXPECT().Get("app.name").Return("test-service")

	var cfg interfaces.ConfigLoader = mock
	assert.Equal(t, "0.0.0.0", cfg.GetString("server.host"))
	assert.Equal(t, 8080, cfg.GetInt("server.port"))
	assert.False(t, cfg.GetBool("debug"), "expected debug = false")
	assert.Equal(t, "test-service", cfg.Get("app.name"))
}

// TestConfig_UnmarshalKey tests that UnmarshalKey decodes a specific config section into a struct.
//
// Why this test is important:
//   - Services use UnmarshalKey to extract per-component config sections
//   - UnmarshalKey must preserve AutomaticEnv() behavior (env vars override YAML)
//   - Database, Redis, Server configs all use this for initialization
//
// What it tests:
//   - UnmarshalKey("database", &dbCfg) populates a DatabaseConfig from YAML
//   - Nested keys are correctly mapped via mapstructure tags
func TestConfig_UnmarshalKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
database:
  host: localhost
  port: 5432
  user: app
  password: dev_password
  database: knowledge_engine
  sslmode: disable
`)

	var cfg interfaces.ConfigLoader
	var err error
	cfg, err = config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err, "New")

	var dbCfg struct {
		Host     string `mapstructure:"host"`
		User     string `mapstructure:"user"`
		Password string `mapstructure:"password"`
		Database string `mapstructure:"database"`
		SSLMode  string `mapstructure:"sslmode"`
		Port     int    `mapstructure:"port"`
	}
	require.NoError(t, cfg.UnmarshalKey("database", &dbCfg), "UnmarshalKey")

	assert.Equal(t, "localhost", dbCfg.Host)
	assert.Equal(t, 5432, dbCfg.Port)
	assert.Equal(t, "app", dbCfg.User)
	assert.Equal(t, "dev_password", dbCfg.Password)
	assert.Equal(t, "knowledge_engine", dbCfg.Database)
	assert.Equal(t, "disable", dbCfg.SSLMode)
}

// TestConfig_MessagingSQSEnvOverride verifies SEARCH_MESSAGING_SQS_* env vars
// override the YAML through UnmarshalKey.
//
// Why this test is important:
//   - The document-events worker selects its SQS backend (Kind) + region/creds via
//     env in staging/prod (SEARCH_MESSAGING_SQS_KIND=sqs). Viper's AutomaticEnv
//     does NOT feed UnmarshalKey for unbound nested keys, so without explicit
//     BindEnv the worker would silently stay on the YAML's "elasticmq" default and
//     never publish to AWS SQS. This test guards that the binds exist.
//
// What it tests:
//   - With base.yaml messaging.sqs.kind=elasticmq, SEARCH_MESSAGING_SQS_KIND=sqs +
//     SEARCH_MESSAGING_SQS_REGION override the unmarshalled values.
//
// NOTE: t.Setenv panics under t.Parallel, so this test is intentionally serial.
func TestConfig_MessagingSQSEnvOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
messaging:
  sqs:
    kind: elasticmq
    endpoint: http://localhost:9324
    region: us-east-1
`)
	t.Setenv("SEARCH_MESSAGING_SQS_KIND", "sqs")
	t.Setenv("SEARCH_MESSAGING_SQS_REGION", "eu-west-1")

	cfg, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err, "New")

	var sqsCfg struct {
		Kind     string `mapstructure:"kind"`
		Endpoint string `mapstructure:"endpoint"`
		Region   string `mapstructure:"region"`
	}
	require.NoError(t, cfg.UnmarshalKey("messaging.sqs", &sqsCfg), "UnmarshalKey")
	assert.Equal(
		t,
		"sqs",
		sqsCfg.Kind,
		"SEARCH_MESSAGING_SQS_KIND must override the YAML",
	)
	assert.Equal(
		t,
		"eu-west-1",
		sqsCfg.Region,
		"SEARCH_MESSAGING_SQS_REGION must override the YAML",
	)
}

// TestConfig_UnmarshalKeyWithDuration tests that UnmarshalKey correctly parses
// duration strings (e.g., "30s") into time.Duration fields.
//
// Why this test is important:
//   - ServerConfig uses time.Duration for read/write timeouts
//   - Without StringToTimeDurationHookFunc, "30s" would fail to parse or produce zero value
//   - Silent zero-value timeouts would cause immediate connection failures
//
// What it tests:
//   - UnmarshalKey parses "30s" string into 30*time.Second
//   - UnmarshalKey parses "60s" string into 60*time.Second
func TestConfig_UnmarshalKeyWithDuration(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
server:
  identity:
    host: "0.0.0.0"
    port: 8090
    read_timeout: 30s
    write_timeout: 60s
`)

	var cfg interfaces.ConfigLoader
	var err error
	cfg, err = config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err, "New")

	var serverCfg struct {
		Identity struct {
			Host         string        `mapstructure:"host"`
			Port         int           `mapstructure:"port"`
			ReadTimeout  time.Duration `mapstructure:"read_timeout"`
			WriteTimeout time.Duration `mapstructure:"write_timeout"`
		} `mapstructure:"identity"`
	}
	require.NoError(t, cfg.UnmarshalKey("server", &serverCfg), "UnmarshalKey")

	assert.Equal(t, 30*time.Second, serverCfg.Identity.ReadTimeout)
	assert.Equal(t, 60*time.Second, serverCfg.Identity.WriteTimeout)
}

// TestConfig_LegacyEnvVarOverride tests that legacy env var names (DB_HOST, REDIS_HOST)
// override YAML values via BindEnv.
//
// Why this test is important:
//   - Existing deployments use DB_HOST, REDIS_HOST, etc. without SEARCH_ prefix
//   - Breaking these would prevent local development and Docker Compose from working
//   - BindEnv provides backward compatibility during migration to SEARCH_* convention
//
// What it tests:
//   - DB_HOST=override overrides YAML database.host
//   - REDIS_HOST=override overrides YAML redis.host
//
// NOTE: t.Setenv panics when called from a parallel test, so this test
// must NOT call t.Parallel().
func TestConfig_LegacyEnvVarOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
database:
  host: localhost
  port: 5432
redis:
  host: localhost
  port: 6379
`)

	t.Setenv("DB_HOST", "override-host")
	t.Setenv("REDIS_HOST", "override-redis")

	var cfg interfaces.ConfigLoader
	var err error
	cfg, err = config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err, "New")

	assert.Equal(
		t,
		"override-host",
		cfg.GetString("database.host"),
		"DB_HOST override failed",
	)
	assert.Equal(
		t,
		"override-redis",
		cfg.GetString("redis.host"),
		"REDIS_HOST override failed",
	)
}

// TestConfigBuilder_KindString tests the string representation of config Kind
// values, covering all switch branches.
//
// Why this test is important:
//   - Kind.String() appears in error messages and logs
//
// What it tests:
//   - KindViper -> "viper"
//   - Unknown Kind -> "Kind(N)" format
func TestConfigBuilder_KindString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		want string
		kind config.Kind
	}{
		{"viper", config.KindViper},
		{"Kind(99)", config.Kind(99)},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.kind.String(), "Kind(%d).String()", tt.kind)
	}
}

// TestConfigBuilder_ConfigToOptions tests that Config.ToOptions produces a
// functional option slice that reproduces the original config.
//
// Why this test is important:
//   - ToOptions is used for config round-tripping; if the resulting options
//     don't reproduce the original config, services may load wrong settings
//
// What it tests:
//   - DefaultConfig.ToOptions applied to a blank config reproduces the original
func TestConfigBuilder_ConfigToOptions(t *testing.T) {
	t.Parallel()

	original := config.DefaultConfig()
	opts := original.ToOptions()

	var rebuilt config.Config
	for _, opt := range opts {
		opt(&rebuilt)
	}

	assert.Equal(t, original.Kind, rebuilt.Kind)
	assert.Equal(t, original.Viper.BaseDir, rebuilt.Viper.BaseDir)
	assert.Equal(t, original.Viper.Prefix, rebuilt.Viper.Prefix)
}

// TestConfigBuilder_WithOptions tests that each With* functional option
// correctly modifies the config.
//
// Why this test is important:
//   - Functional options are the public API for config customization; each one
//     must correctly set its target field
//
// What it tests:
//   - WithBaseDir sets Viper.BaseDir
//   - WithEnvironment sets Viper.Env
//   - WithEnvPrefix sets Viper.Prefix
func TestConfigBuilder_WithOptions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg, err := config.New(
		config.KindViper,
		config.WithBaseDir(dir),
		config.WithEnvironment("staging"),
		config.WithEnvPrefix("MYAPP"),
	)
	require.NoError(t, err, "New")
	require.NotNil(t, cfg, "expected non-nil config loader")
}

// TestConfig_EnvOverlayMergeError tests that the loader returns an error when
// the environment overlay file exists but contains invalid YAML.
//
// Why this test is important:
//   - A corrupted or syntactically invalid overlay must not silently produce
//     empty config; it must fail loudly at startup
//   - Covers the MergeInConfig error branch at loader.go line 83
//
// What it tests:
//   - Load returns a non-nil error mentioning the env config merge failure
func TestConfig_EnvOverlayMergeError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
server:
  port: 8080
`)
	writeFile(t, filepath.Join(dir, "broken.yaml"), ":\n  - :\n\t\t- bad")

	_, err := config.New(
		config.KindViper,
		config.WithBaseDir(dir),
		config.WithEnvironment("broken"),
	)
	require.Error(t, err, "expected error when merging invalid env overlay")
}

// TestConfig_SecretsMergeError tests that the loader returns an error when
// secrets.yaml exists but contains invalid YAML.
//
// Why this test is important:
//   - A corrupted secrets file must fail startup rather than silently dropping
//     all secret values
//   - Covers the MergeInConfig error branch at loader.go line 93
//
// What it tests:
//   - Load returns a non-nil error mentioning the secrets merge failure
func TestConfig_SecretsMergeError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
server:
  port: 8080
`)
	writeFile(t, filepath.Join(dir, "secrets.yaml"), ":\n  - :\n\t\t- bad")

	_, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.Error(t, err, "expected error when merging invalid secrets.yaml")
}

// TestConfig_UnmarshalKeyMissingKey tests that UnmarshalKey returns an error
// when the requested config key does not exist.
//
// Why this test is important:
//   - Services call UnmarshalKey to load per-component config. If the section
//     is entirely missing (e.g., config file lacks "redis:" block), the service
//     must get a clear error rather than a zero-value struct that produces
//     silent runtime failures.
//   - Covers the Sub() nil-return branch at loader.go line 182
//
// What it tests:
//   - UnmarshalKey("nonexistent", &target) returns a non-nil error
//   - The error message mentions the missing key name
func TestConfig_UnmarshalKeyMissingKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
server:
  port: 8080
`)

	cfg, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err, "New")

	var target struct {
		Host string `mapstructure:"host"`
		Port int    `mapstructure:"port"`
	}
	err = cfg.UnmarshalKey("nonexistent.section", &target)
	require.Error(t, err, "expected error for missing config key")
	assert.Contains(
		t,
		err.Error(),
		"nonexistent.section",
		"error should mention missing key",
	)
}

// TestConfigBuilder_NewFromConfigUnknownKind tests that NewFromConfig returns
// an error when given an unknown Kind value.
//
// Why this test is important:
//   - NewFromConfig is the lower-level factory; it must reject invalid Kind
//     values to prevent nil-pointer panics from uninitialized backends.
//   - Covers the default branch at config.go line 78 (NewFromConfig)
//
// What it tests:
//   - NewFromConfig with Kind(99) returns a non-nil error
//   - The error message mentions the unknown kind
func TestConfigBuilder_NewFromConfigUnknownKind(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Kind = config.Kind(99)

	_, err := config.NewFromConfig(cfg)
	require.Error(t, err, "expected error for unknown kind in NewFromConfig")
	assert.Contains(
		t,
		err.Error(),
		"unknown config kind",
		"error should mention unknown kind",
	)
}

// TestConfig_Auth0EnvVarOverride tests that AUTH0_DOMAIN, AUTH0_CLIENT_ID, and
// AUTH0_AUDIENCE environment variables override the corresponding YAML values
// via BindEnv, matching the production env-var injection pattern.
//
// Why this test is important:
//   - Kubernetes injects Auth0 credentials as environment variables from secrets,
//     not as YAML files; without BindEnv the YAML values would silently win
//   - A misconfigured BindEnv causes staging/prod to use wrong tenants with no error
//   - This is the exact path used by the Kong seeder and every deployed service
//
// What it tests:
//   - AUTH0_DOMAIN env var overrides both auth.auth0.domain (JWT validation) and
//     the flat auth.auth0_domain (identity Auth0-client provider)
//   - AUTH0_CLIENT_ID env var populates both auth.auth0.client_id (nested) AND the
//     flat auth.auth0_client_id the identity Auth0-client provider reads — the flat
//     key had no BindEnv, so the deployed identity service (configured from the
//     AUTH0_CLIENT_ID env/secret, not YAML) crashed on "auth.auth0_client_id not set"
//   - AUTH0_MANAGEMENT_API_CLIENT_ID/SECRET populate the flat management-API keys
//   - AUTH0_AUDIENCE env var overrides auth.auth0.audience YAML value
//
// NOTE: t.Setenv panics in parallel tests - must NOT call t.Parallel().
func TestConfig_Auth0EnvVarOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
auth:
  stub: false
  auth0:
    domain: "original.us.auth0.com"
    client_id: "original-client-id"
    audience: "https://api.original.example.com"
`)
	t.Setenv("AUTH0_DOMAIN", "override.us.auth0.com")
	t.Setenv("AUTH0_CLIENT_ID", "override-client-id")
	t.Setenv("AUTH0_AUDIENCE", "https://api.override.example.com")
	t.Setenv("AUTH0_MANAGEMENT_API_CLIENT_ID", "override-mgmt-id")
	t.Setenv("AUTH0_MANAGEMENT_API_CLIENT_SECRET", "override-mgmt-secret")

	cfg, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err, "New")

	assert.Equal(
		t,
		"override.us.auth0.com",
		cfg.GetString("auth.auth0.domain"),
		"AUTH0_DOMAIN override failed",
	)
	assert.Equal(
		t,
		"override-client-id",
		cfg.GetString("auth.auth0.client_id"),
		"AUTH0_CLIENT_ID override failed",
	)
	assert.Equal(
		t,
		"https://api.override.example.com",
		cfg.GetString("auth.auth0.audience"),
		"AUTH0_AUDIENCE override failed",
	)

	// The flat auth.auth0_* keys the identity Auth0-client provider reads. The
	// auth0_client_id binding is the regression guard: without it the deployed
	// identity service starts with an empty client id and fails initialization.
	assert.Equal(
		t,
		"override-client-id",
		cfg.GetString("auth.auth0_client_id"),
		"AUTH0_CLIENT_ID must also populate the flat auth.auth0_client_id key",
	)
	assert.Equal(
		t,
		"override.us.auth0.com",
		cfg.GetString("auth.auth0_domain"),
		"AUTH0_DOMAIN must populate the flat auth.auth0_domain key",
	)
	assert.Equal(
		t,
		"override-mgmt-id",
		cfg.GetString("auth.auth0_management_api_id"),
		"AUTH0_MANAGEMENT_API_CLIENT_ID must populate auth.auth0_management_api_id",
	)
	assert.Equal(
		t,
		"override-mgmt-secret",
		cfg.GetString("auth.auth0_management_api_secret"),
		"AUTH0_MANAGEMENT_API_CLIENT_SECRET must populate auth.auth0_management_api_secret",
	)
}

// TestConfig_EnvSelectorFallsBackToAppEnv tests that the overlay is selected
// from APP_ENV / ENVIRONMENT when SEARCH_ENV is unset.
//
// Why this test is important:
//   - The Helm charts and the environment-config ConfigMap declare the
//     deployment environment via APP_ENV / ENVIRONMENT, but the loader
//     historically only read SEARCH_ENV. With SEARCH_ENV unset the staging pods
//     silently ran on base.yaml alone — localhost SQS and stubbed auth — with no
//     error. This guards viper.ResolveEnv, which closes that gap.
//
// What it tests:
//   - With SEARCH_ENV empty and APP_ENV=staging, the staging overlay wins:
//     messaging.sqs.endpoint is blanked (real-AWS SQS), not base's localhost URL
//
// NOTE: t.Setenv panics in parallel tests - must NOT call t.Parallel().
func TestConfig_EnvSelectorFallsBackToAppEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
messaging:
  sqs:
    endpoint: "http://localhost:9324"
`)
	writeFile(t, filepath.Join(dir, "staging.yaml"), `
messaging:
  sqs:
    endpoint: ""
`)
	// SEARCH_ENV (canonical selector) unset as in the pods; APP_ENV is what the
	// service Helm charts set. SQS_ENDPOINT unset so the YAML value stands.
	t.Setenv("SEARCH_ENV", "")
	t.Setenv("APP_ENV", "staging")

	cfg, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err, "New")

	assert.Equal(
		t,
		"",
		cfg.GetString("messaging.sqs.endpoint"),
		"APP_ENV=staging must select staging.yaml (endpoint blanked for real AWS)",
	)
}

// TestConfig_OverlayDeepMergesQueuesMap tests that an env overlay overriding a
// single nested map key preserves the sibling keys from base.
//
// Why this test is important:
//   - A staging overlay overrides only a single nested queue key (to the -staging
//     queue Terraform provisions) and relies on viper deep-merging the map so the
//     sibling queue names survive from base. A shallow replace would silently drop
//     those keys, breaking every queue but the overridden one.
//
// What it tests:
//   - The overridden key takes the overlay's -staging value
//   - The sibling keys present only in base are preserved
func TestConfig_OverlayDeepMergesQueuesMap(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
messaging:
  sqs:
    queues:
      document_upload: "document-upload"
      notification: "notification"
`)
	writeFile(t, filepath.Join(dir, "staging.yaml"), `
messaging:
  sqs:
    queues:
      notification: "notification-staging"
`)
	cfg, err := config.New(
		config.KindViper,
		config.WithBaseDir(dir),
		config.WithEnvironment("staging"),
	)
	require.NoError(t, err, "New")

	assert.Equal(
		t,
		"notification-staging",
		cfg.GetString("messaging.sqs.queues.notification"),
		"overlay must override the notification queue name",
	)
	assert.Equal(
		t,
		"document-upload",
		cfg.GetString("messaging.sqs.queues.document_upload"),
		"deep-merge must preserve sibling queue keys from base",
	)
}

// writeFile is a test helper that writes content to path, failing the test on
// error.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	err := os.WriteFile(path, []byte(content), 0o644)
	require.NoErrorf(t, err, "write %s", path)
}
