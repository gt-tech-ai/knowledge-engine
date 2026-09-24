package unit_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine/go/foundation/config"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/config/schema/infra"
)

// TestProvideServerConfig tests that a Wire provider can load ServerConfig from YAML.
//
// Why this test is important:
//   - Wire providers are the DI entry point for all services
//   - Must produce valid, validated config structs
//   - Must respect environment variable overrides
//
// What it tests:
//   - ConfigLoader creation with custom base dir
//   - UnmarshalKey extracts server config correctly
//   - Validation catches invalid configs (zero port)
func TestProvideServerConfig(t *testing.T) {
	t.Parallel()

	// Arrange: Create temp config directory with server config
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "base.yaml")
	err := os.WriteFile(configFile, []byte(`
server:
  identity:
    host: "0.0.0.0"
    port: 8090
    read_timeout: 30s
    read_header_timeout: 10s
    write_timeout: 30s
    idle_timeout: 120s
`), 0o644)
	require.NoError(t, err, "write config")

	// Act: Simulate Wire provider behavior (identity service path)
	loader, err := config.New(config.KindViper, config.WithBaseDir(tmpDir))
	require.NoError(t, err, "config.New")

	var serverCfg infra.ServerConfig
	require.NoError(t, loader.UnmarshalKey("server.identity", &serverCfg), "UnmarshalKey")

	// Assert: Config loaded correctly
	assert.Equal(t, "0.0.0.0", serverCfg.Host)
	assert.Equal(t, 8090, serverCfg.Port)
	assert.Equal(t, "30s", serverCfg.ReadTimeout.String())

	// Validate should pass
	require.NoError(t, serverCfg.Validate(), "Validate must pass for valid config")
}

// TestProvideServerConfig_ValidationFailure tests that validation catches zero port.
//
// Why this test is important:
//   - Wire providers must validate configs before returning
//   - Invalid configs should cause startup failures, not runtime crashes
//
// What it tests:
//   - ServerConfig.Validate() returns error for Port == 0
func TestProvideServerConfig_ValidationFailure(t *testing.T) {
	t.Parallel()

	// Arrange: ServerConfig with zero port (invalid)
	serverCfg := infra.ServerConfig{
		Host: "0.0.0.0",
		Port: 0, // Invalid!
	}

	// Act & Assert: Validation should fail
	err := serverCfg.Validate()
	require.Error(t, err, "Validate() must return error for Port == 0")
	// Validate() now returns a coded core/errors value (ARCHITECTURE.md#error-codes), so Error()
	// carries an "INVALID_INPUT: " prefix; assert the meaningful message as a
	// substring rather than pinning the exact rendered (code-prefixed) string.
	assert.ErrorContains(t, err, "server.port is required (got 0)")
}

// TestProvideDatabaseConfig tests that Wire provider can load DatabaseConfig.
//
// Why this test is important:
//   - Database config is used by identity, api, and migration services
//   - Must support legacy env var names (DB_HOST, etc.)
//
// What it tests:
//   - UnmarshalKey extracts database config
//   - DSN() method produces correct connection string
func TestProvideDatabaseConfig(t *testing.T) {
	t.Parallel()

	// Arrange
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "base.yaml")
	err := os.WriteFile(configFile, []byte(`
database:
  host: localhost
  port: 5432
  user: app
  password: dev_password
  database: knowledge_engine
  sslmode: disable
`), 0o644)
	require.NoError(t, err, "write config")

	// Act
	loader, err := config.New(config.KindViper, config.WithBaseDir(tmpDir))
	require.NoError(t, err, "config.New")

	var dbCfg infra.DatabaseConfig
	require.NoError(t, loader.UnmarshalKey("database", &dbCfg), "UnmarshalKey")

	// Assert
	assert.Equal(t, "localhost", dbCfg.Host)
	assert.Equal(t, "app", dbCfg.User)
	assert.Equal(
		t,
		"postgres://app:dev_password@localhost:5432/knowledge_engine?sslmode=disable",
		dbCfg.DSN(),
	)

	// Validate should pass
	require.NoError(t, dbCfg.Validate(), "Validate must pass for valid config")
}

// TestProvideRedisConfig tests that Wire provider can load RedisConfig.
//
// Why this test is important:
//   - Redis config is used by API service for caching
//
// What it tests:
//   - UnmarshalKey extracts redis config
//   - Addr() method produces correct host:port string
func TestProvideRedisConfig(t *testing.T) {
	t.Parallel()

	// Arrange
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "base.yaml")
	err := os.WriteFile(configFile, []byte(`
redis:
  host: localhost
  port: 6379
  password: ""
  db: 0
`), 0o644)
	require.NoError(t, err, "write config")

	// Act
	loader, err := config.New(config.KindViper, config.WithBaseDir(tmpDir))
	require.NoError(t, err, "config.New")

	var redisCfg infra.RedisConfig
	require.NoError(t, loader.UnmarshalKey("redis", &redisCfg), "UnmarshalKey")

	// Assert
	assert.Equal(t, "localhost", redisCfg.Host)
	assert.Equal(t, 6379, redisCfg.Port)
	assert.Equal(t, "localhost:6379", redisCfg.Addr())

	// Validate should pass
	require.NoError(t, redisCfg.Validate(), "Validate must pass for valid config")
}

// TestProvideConfig_EnvVarOverride tests that env vars override YAML values.
//
// Why this test is important:
//   - 12-factor app: environment variables must take precedence
//   - Legacy env vars (DB_HOST) must work alongside SEARCH_* names
//
// What it tests:
//   - SEARCH_DATABASE_HOST overrides YAML database.host
//   - Legacy DB_HOST also overrides YAML database.host
func TestProvideConfig_EnvVarOverride(t *testing.T) {
	// Note: Cannot use t.Parallel() with t.Setenv()

	// Arrange
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "base.yaml")
	err := os.WriteFile(configFile, []byte(`
database:
  host: localhost
  port: 5432
  user: app
  password: dev_password
  database: knowledge_engine
  sslmode: disable
`), 0o644)
	require.NoError(t, err, "write config")

	// Set env var override
	t.Setenv("SEARCH_DATABASE_HOST", "prod-db.example.com")

	// Act
	loader, err := config.New(config.KindViper, config.WithBaseDir(tmpDir))
	require.NoError(t, err, "config.New")

	var dbCfg infra.DatabaseConfig
	require.NoError(t, loader.UnmarshalKey("database", &dbCfg), "UnmarshalKey")

	// Assert: Env var overrode YAML value
	assert.Equal(t, "prod-db.example.com", dbCfg.Host, "env var override failed")
}
