package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine/go/foundation/config/schema/infra"
)

// TestDatabaseConfig_Defaults tests that DefaultDatabaseConfig returns correct defaults.
//
// Why this test is important:
//   - DatabaseConfig is used by every Go service that connects to PostgreSQL
//   - Incorrect defaults would break local development (must match Docker Compose)
//   - SSLMode default must be "disable" for local dev, configurable for prod
//
// What it tests:
//   - Host defaults to "localhost"
//   - Port defaults to 5432
//   - User defaults to "app" (matches Docker Compose)
//   - Password defaults to "dev_password" (matches Docker Compose)
//   - Database defaults to "knowledge_engine"
//   - SSLMode defaults to "disable"
func TestDatabaseConfig_Defaults(t *testing.T) {
	t.Parallel()

	cfg := infra.DefaultDatabaseConfig()
	assert.Equal(t, "localhost", cfg.Host)
	assert.Equal(t, 5432, cfg.Port)
	assert.Equal(t, "app", cfg.User)
	assert.Equal(t, "dev_password", cfg.Password)
	assert.Equal(t, "knowledge_engine", cfg.Database)
	assert.Equal(t, "disable", cfg.SSLMode)
}

// TestDatabaseConfig_DSN tests that DSN() constructs a valid PostgreSQL connection string.
//
// Why this test is important:
//   - DSN is used directly by database/sql to open connections
//   - Incorrect format would prevent all database operations
//   - SSLMode parameter is critical for security (prod) and ease-of-use (dev)
//
// What it tests:
//   - DSN format: postgres://user:password@host:port/database?sslmode=disable
//   - SSLMode parameter is included in the query string
func TestDatabaseConfig_DSN(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
		cfg  infra.DatabaseConfig
	}{
		{
			name: "default config",
			cfg:  infra.DefaultDatabaseConfig(),
			want: "postgres://app:dev_password@localhost:5432/knowledge_engine?sslmode=disable",
		},
		{
			name: "custom config with sslmode=require",
			cfg: infra.DatabaseConfig{
				Host:     "prod-db.example.com",
				Port:     5432,
				User:     "prod_user",
				Password: "prod_pass",
				Database: "prod_db",
				SSLMode:  "require",
			},
			want: "postgres://prod_user:prod_pass@prod-db.example.com:5432/prod_db?sslmode=require",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.cfg.DSN())
		})
	}
}

// TestDatabaseConfig_Validate tests that Validate returns an error for invalid configurations.
//
// Why this test is important:
//   - Services must fail fast at startup with clear errors, not runtime panics
//   - Empty Host would cause connection failures with opaque network errors
//   - Validation catches misconfiguration before any database calls are attempted
//
// What it tests:
//   - Validate returns error when Host is empty
//   - Validate returns nil when Host is non-empty
func TestDatabaseConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     infra.DatabaseConfig
		wantErr bool
	}{
		{
			name:    "valid config",
			cfg:     infra.DefaultDatabaseConfig(),
			wantErr: false,
		},
		{
			name: "empty host",
			cfg: infra.DatabaseConfig{
				Host:     "",
				Port:     5432,
				User:     "app",
				Password: "dev_password",
				Database: "knowledge_engine",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestRedisConfig_Defaults tests that DefaultRedisConfig returns correct defaults.
//
// Why this test is important:
//   - RedisConfig is used by API service for caching and session storage
//   - Incorrect defaults would break cache operations silently
//
// What it tests:
//   - Host defaults to "localhost"
//   - Port defaults to 6379
//   - DB defaults to 0
func TestRedisConfig_Defaults(t *testing.T) {
	t.Parallel()

	cfg := infra.DefaultRedisConfig()
	assert.Equal(t, "localhost", cfg.Host)
	assert.Equal(t, 6379, cfg.Port)
	assert.Equal(t, 0, cfg.DB)
}

// TestRedisConfig_Addr tests that Addr() returns host:port format.
//
// Why this test is important:
//   - Redis clients expect "host:port" format for the address
//   - Incorrect format would prevent Redis connections
//
// What it tests:
//   - Addr returns "localhost:6379" for default config
//   - Addr returns correct format for custom host/port
func TestRedisConfig_Addr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
		cfg  infra.RedisConfig
	}{
		{
			name: "default config",
			cfg:  infra.DefaultRedisConfig(),
			want: "localhost:6379",
		},
		{
			name: "custom config",
			cfg: infra.RedisConfig{
				Host: "redis.example.com",
				Port: 6380,
			},
			want: "redis.example.com:6380",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.cfg.Addr())
		})
	}
}

// TestServerConfig_Defaults tests that DefaultServerConfig returns correct defaults.
//
// Why this test is important:
//   - ServerConfig is used by all Go server services (identity, api)
//   - Timeouts prevent hung requests from blocking goroutines indefinitely
//
// What it tests:
//   - Host defaults to "0.0.0.0"
//   - Port defaults to 0 (must be overridden per-service)
//   - ReadTimeout defaults to 30s
//   - WriteTimeout defaults to 30s
func TestServerConfig_Defaults(t *testing.T) {
	t.Parallel()

	cfg := infra.DefaultServerConfig()
	assert.Equal(t, "0.0.0.0", cfg.Host)
	assert.Equal(t, 0, cfg.Port)
	assert.Equal(t, 30*time.Second, cfg.ReadTimeout)
	assert.Equal(t, 30*time.Second, cfg.WriteTimeout)
}

// TestServerConfig_Validate tests that Validate returns an error when Port is 0.
//
// Why this test is important:
//   - Port 0 means "random port" which is rarely desired for production services
//   - Services should explicitly configure their port via YAML or env vars
//   - Early validation prevents accidental "it works on my machine" scenarios
//
// What it tests:
//   - Validate returns error when Port is 0
//   - Validate returns nil when Port is non-zero
func TestServerConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     infra.ServerConfig
		wantErr bool
	}{
		{
			name: "valid config",
			cfg: infra.ServerConfig{
				Host: "0.0.0.0",
				Port: 8090,
			},
			wantErr: false,
		},
		{
			name: "port is zero",
			cfg: infra.ServerConfig{
				Host: "0.0.0.0",
				Port: 0,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestS3Config_Defaults tests that DefaultS3Config returns correct defaults.
//
// Why this test is important:
//   - S3Config is used for document storage
//   - Defaults must match local MinIO setup for development
//
// What it tests:
//   - Endpoint defaults to "http://localhost:9000"
//   - Bucket defaults to "documents"
//   - Region defaults to "us-east-1"
func TestS3Config_Defaults(t *testing.T) {
	t.Parallel()

	cfg := infra.DefaultS3Config()
	assert.Equal(t, "http://localhost:9000", cfg.Endpoint)
	assert.Equal(t, "documents", cfg.Bucket)
	assert.Equal(t, "us-east-1", cfg.Region)
}

// TestSQSConfig_Defaults tests that DefaultSQSConfig returns correct defaults.
//
// Why this test is important:
//   - SQSConfig is used for async messaging (ingestion, notifications)
//   - Defaults must match local ElasticMQ setup for development
//
// What it tests:
//   - Endpoint defaults to "http://localhost:9324"
//   - Region defaults to "us-east-1"
//   - Static dev credentials default to "local"
//   - The document/notification queue names resolve (consumers/publishers key off
//     this map; a missing entry silently disables that flow)
func TestSQSConfig_Defaults(t *testing.T) {
	t.Parallel()

	cfg := infra.DefaultSQSConfig()
	assert.Equal(t, "http://localhost:9324", cfg.Endpoint)
	assert.Equal(t, "us-east-1", cfg.Region)
	assert.Equal(t, "local", cfg.AccessKeyID)
	assert.Equal(t, "local", cfg.SecretAccessKey)
	assert.Equal(t, "document-upload", cfg.Queues["document_upload"])
	assert.Equal(t, "notification", cfg.Queues["notification"])
}

// TestLoggingConfig_Defaults tests that DefaultLoggingConfig returns correct defaults.
//
// Why this test is important:
//   - LoggingConfig determines observability for all services
//   - Wrong defaults would make debugging impossible or leak PII
//
// What it tests:
//   - Level defaults to "info"
//   - Format defaults to "json"
//   - RedactPII defaults to true
func TestLoggingConfig_Defaults(t *testing.T) {
	t.Parallel()

	cfg := infra.DefaultLoggingConfig()
	assert.Equal(t, "info", cfg.Level)
	assert.Equal(t, "json", cfg.Format)
	assert.True(t, cfg.RedactPII, "RedactPII must default to true")
}

// TestObservabilityConfig_Defaults tests that DefaultObservabilityConfig returns correct defaults.
//
// Why this test is important:
//   - ObservabilityConfig enables tracing and metrics
//   - Defaults must work for local dev (Alloy on localhost:4317)
//
// What it tests:
//   - Tracing.Enabled defaults to true
//   - Tracing.Endpoint defaults to "localhost:4317"
//   - Tracing.SampleRate defaults to 0.1 (10%)
//   - Metrics.Enabled defaults to true
//   - Metrics.Port defaults to 9090
func TestObservabilityConfig_Defaults(t *testing.T) {
	t.Parallel()

	cfg := infra.DefaultObservabilityConfig()
	assert.True(t, cfg.Tracing.Enabled, "Tracing.Enabled must default to true")
	assert.Equal(t, "localhost:4317", cfg.Tracing.Endpoint)
	assert.Equal(t, 0.1, cfg.Tracing.SampleRate)
	assert.True(t, cfg.Metrics.Enabled, "Metrics.Enabled must default to true")
	assert.Equal(t, 9090, cfg.Metrics.Port)
}

// ---------------------------------------------------------------------------
// Validate methods - remaining config structs
// ---------------------------------------------------------------------------

// TestAppConfig_DefaultAndValidate tests that DefaultAppConfig returns a valid
// config with non-empty Name and Environment, and that Validate passes.
//
// Why this test is important:
//   - AppConfig.Name is used in log lines, trace labels, and service registration
//   - AppConfig.Environment drives YAML overlay selection (dev/staging/prod)
//   - A blank Name or Environment causes opaque errors or wrong overlays at startup
//
// What it tests:
//   - DefaultAppConfig().Name is non-empty
//   - DefaultAppConfig().Environment is non-empty
//   - Validate() returns nil for the default config
func TestAppConfig_DefaultAndValidate(t *testing.T) {
	t.Parallel()

	cfg := infra.DefaultAppConfig()
	assert.NotEmpty(t, cfg.Name, "DefaultAppConfig().Name must not be empty")
	assert.NotEmpty(
		t,
		cfg.Environment,
		"DefaultAppConfig().Environment must not be empty",
	)
	require.NoError(
		t,
		cfg.Validate(),
		"AppConfig.Validate() must return nil for default config",
	)
}

// TestAuthConfig_Validate tests that Validate enforces required Auth0 fields
// when stub mode is disabled.
//
// Why this test is important:
//   - Misconfigured Auth0 settings cause every authenticated request to fail at runtime
//   - Stub mode must bypass validation so local dev doesn't require real Auth0 credentials
//   - Missing domain or audience errors are only surfaced at startup; late discovery
//     (e.g., during a live request) is far harder to diagnose
//
// What it tests:
//   - stub=true skips domain and audience checks (returns nil)
//   - Full non-stub config (domain + audience) passes validation
//   - Missing domain with stub=false returns an error
//   - Missing audience with stub=false returns an error
//   - service_stub=true with stub=false is rejected — a real deploy (end-user auth
//
// enforced) must not leave service-to-service auth bypassed (fail-open guard)
func TestAuthConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     infra.AuthConfig
		wantErr bool
	}{
		{
			name:    "stub mode skips domain/audience check",
			cfg:     infra.AuthConfig{Stub: true},
			wantErr: false,
		},
		{
			name: "valid non-stub config",
			cfg: infra.AuthConfig{
				Auth0: infra.Auth0Config{
					Domain:   "example.us.auth0.com",
					Audience: "https://api.dev.example.com",
				},
			},
			wantErr: false,
		},
		{
			name: "missing domain when stub=false",
			cfg: infra.AuthConfig{
				Auth0: infra.Auth0Config{Audience: "https://api.dev.example.com"},
			},
			wantErr: true,
		},
		{
			name: "missing audience when stub=false",
			cfg: infra.AuthConfig{
				Auth0: infra.Auth0Config{Domain: "example.us.auth0.com"},
			},
			wantErr: true,
		},
		{
			name: "service_stub allowed in stub mode",
			cfg:  infra.AuthConfig{Stub: true, ServiceStub: true},
			// dev: both bypasses on together is fine
			wantErr: false,
		},
		{
			name: "service_stub rejected when stub=false",
			cfg: infra.AuthConfig{
				Auth0: infra.Auth0Config{
					Domain:   "example.us.auth0.com",
					Audience: "https://api.dev.example.com",
				},
				ServiceStub: true,
			},
			// deploy (stub=false) must not bypass service auth
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestDefaultAuthConfig_ServiceStubEnabled tests that the local-dev default enables
// the service-to-service auth bypass alongside the end-user stub.
//
// Why this test is important:
//   - `search dev up` must work with no service tokens provisioned; the default must
//
// bypass service auth (dev bypass), exactly as it bypasses end-user auth
//   - a default that enforced service auth would break every local internal RPC
//
// What it tests:
//   - DefaultAuthConfig() sets both Stub and ServiceStub to true
func TestDefaultAuthConfig_ServiceStubEnabled(t *testing.T) {
	t.Parallel()
	cfg := infra.DefaultAuthConfig()
	require.True(t, cfg.Stub, "default must enable end-user stub for local dev")
	require.True(
		t,
		cfg.ServiceStub,
		"default must enable service-auth stub for local dev",
	)
}

// TestAuthConfig_DefaultAuthConfig tests that DefaultAuthConfig returns stub=true
// as the safe local-dev default before Auth0 credentials are configured.
//
// Why this test is important:
//   - The default must enable stub mode so local dev works out-of-the-box without Auth0
//   - If the default were stub=false, every developer would need Auth0 credentials just to run
//
// What it tests:
//   - DefaultAuthConfig().Stub is true
func TestAuthConfig_DefaultAuthConfig(t *testing.T) {
	t.Parallel()

	cfg := infra.DefaultAuthConfig()
	assert.True(t, cfg.Stub, "DefaultAuthConfig().Stub must be true")
}

// TestLoggingConfig_Validate tests that the default LoggingConfig passes validation.
//
// Why this test is important:
//   - Logging is a critical dependency for all services; invalid config silently
//     disables structured logs, making production incidents impossible to diagnose
//   - Catching validation errors at startup is far cheaper than debugging a silent logger
//
// What it tests:
//   - DefaultLoggingConfig().Validate() returns nil
func TestLoggingConfig_Validate(t *testing.T) {
	t.Parallel()

	cfg := infra.DefaultLoggingConfig()
	require.NoError(t, cfg.Validate())
}

// TestLoggingConfig_Validate_InvalidKind tests that Validate rejects an unknown
// logger kind.
//
// Why this test is important:
//   - Unknown logger kinds would silently fall back to no-op logging, making
//     all production incidents invisible until someone notices missing logs
//
// What it tests:
//   - LoggingConfig{Kind: "logrus"}.Validate() returns an error
func TestLoggingConfig_Validate_InvalidKind(t *testing.T) {
	t.Parallel()

	cfg := infra.LoggingConfig{Kind: "logrus"}
	require.Error(t, cfg.Validate(), "Validate() must return error for unknown kind")
}

// TestLoggingConfig_GetKind tests that GetKind maps known logger kinds and
// returns an error for unknown ones.
//
// Why this test is important:
//   - GetKind is the authoritative mapping from string config to the typed Kind
//     constant; a wrong mapping or missing case silently produces the wrong logger
//
// What it tests:
//   - "zap", "stdlib", and "ZAP" (case-insensitive) return nil error
//   - "logrus" returns a non-nil error
func TestLoggingConfig_GetKind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		kind    string
		wantErr bool
	}{
		{"zap", false},
		{"stdlib", false},
		{"ZAP", false},
		{"logrus", true},
	}
	for _, tc := range cases {
		cfg := infra.LoggingConfig{Kind: tc.kind}
		_, err := cfg.GetKind()
		if tc.wantErr {
			assert.Error(t, err, "GetKind(%q) must return error", tc.kind)
		} else {
			assert.NoError(t, err, "GetKind(%q) must not return error", tc.kind)
		}
	}
}

// TestObservabilityConfig_Validate tests that the default ObservabilityConfig
// passes validation.
//
// Why this test is important:
//   - ObservabilityConfig drives OpenTelemetry tracing and Prometheus metrics
//   - An invalid config at startup would silently disable all observability,
//     making performance issues and errors invisible in production
//
// What it tests:
//   - DefaultObservabilityConfig().Validate() returns nil
func TestObservabilityConfig_Validate(t *testing.T) {
	t.Parallel()

	cfg := infra.DefaultObservabilityConfig()
	require.NoError(t, cfg.Validate())
}

// TestS3Config_Validate tests that the default S3Config passes validation.
//
// Why this test is important:
//   - S3Config is used for document upload and retrieval; invalid config causes
//     every document operation to fail with opaque storage errors at runtime
//   - Startup validation surfaces missing bucket or endpoint before any request fails
//
// What it tests:
//   - DefaultS3Config().Validate() returns nil
func TestS3Config_Validate(t *testing.T) {
	t.Parallel()

	cfg := infra.DefaultS3Config()
	require.NoError(t, cfg.Validate())
}

// TestS3Config_KindAndParityDefaults tests that DefaultS3Config returns the
// MinIO-oriented dev/stage parity defaults (Kind, static credentials, path-style).
//
// Why this test is important:
//   - The storage client selects its backend (MinIO vs AWS S3) from S3Config.Kind;
//     a wrong default silently points local dev at the wrong addressing/credentials
//   - MinIO requires path-style addressing and static credentials, unlike AWS S3
//
// What it tests:
//   - Kind defaults to "minio"
//   - ForcePathStyle defaults to true (required by MinIO)
//   - AccessKeyID/SecretAccessKey default to the local MinIO credentials
//   - PresignExpiry defaults to 15 minutes
func TestS3Config_KindAndParityDefaults(t *testing.T) {
	t.Parallel()

	cfg := infra.DefaultS3Config()
	assert.Equal(t, "minio", cfg.Kind)
	assert.True(t, cfg.ForcePathStyle, "MinIO requires path-style addressing")
	assert.Equal(t, "minioadmin", cfg.AccessKeyID)
	assert.Equal(t, "minioadmin", cfg.SecretAccessKey)
	assert.Equal(t, 15*time.Minute, cfg.PresignExpiry)
}

// TestS3Config_Validate_RejectsUnknownKind tests that Validate rejects a storage
// kind that is neither "s3" nor "minio", and rejects an empty bucket.
//
// Why this test is important:
//   - An unknown kind would construct a client against the wrong backend at runtime
//   - An empty bucket makes every object operation fail with an opaque storage error
//   - Startup validation surfaces these before any request is served
//
// What it tests:
//   - Validate returns an error for an unknown kind
//   - Validate returns an error for an empty bucket
//   - Validate returns nil for a valid s3 config
func TestS3Config_Validate_RejectsUnknownKind(t *testing.T) {
	t.Parallel()

	require.Error(
		t,
		infra.S3Config{Kind: "gcs", Bucket: "b"}.Validate(),
		"unknown kind must error",
	)
	require.Error(
		t,
		infra.S3Config{Kind: "s3", Bucket: ""}.Validate(),
		"empty bucket must error",
	)
	require.NoError(t, infra.S3Config{Kind: "s3", Bucket: "b"}.Validate())
}

// TestS3Config_Validate_MultipartExpiry verifies the documented invariant that a
// multipart part-URL must not expire before a single-part PUT URL.
//
// Why this test is important:
//   - The reaper TTL and cleanup grace are sized above the multipart expiry; a shorter
//     multipart expiry would let a live upload be reaped mid-flight. Validate is the
//     only place that catches the misconfiguration at startup.
//
// What it tests:
//   - multipart_presign_expiry < presign_expiry errors; >= (or unset, 0) is accepted.
//   - EffectiveMultipartPresignExpiry returns the configured value when set, and
//     falls back to 4× presign_expiry when unset — the window the reaper/cleanup
//     grace is sized above.
func TestS3Config_Validate_MultipartExpiry(t *testing.T) {
	t.Parallel()

	base := infra.S3Config{Kind: "s3", Bucket: "b", PresignExpiry: 15 * time.Minute}

	tooShort := base
	tooShort.MultipartPresignExpiry = time.Minute
	require.Error(
		t,
		tooShort.Validate(),
		"multipart expiry below single-PUT expiry must error",
	)

	longer := base
	longer.MultipartPresignExpiry = time.Hour
	require.NoError(t, longer.Validate())
	assert.Equal(t, time.Hour, longer.EffectiveMultipartPresignExpiry(),
		"a configured multipart expiry is used as-is")

	// Unset (0) is accepted — EffectiveMultipartPresignExpiry falls back to 4×.
	require.NoError(t, base.Validate())
	assert.Equal(t, 4*base.PresignExpiry, base.EffectiveMultipartPresignExpiry(),
		"an unset multipart expiry falls back to 4× the single-PUT expiry")
}

// TestSQSConfig_Validate tests that the default SQSConfig passes validation.
//
// Why this test is important:
//   - SQSConfig drives the async messaging pipeline (document ingestion,
//     notifications); invalid config silently breaks all async operations
//   - Catching misconfiguration at startup prevents silent queue starvation
//
// What it tests:
//   - DefaultSQSConfig().Validate() returns nil
func TestSQSConfig_Validate(t *testing.T) {
	t.Parallel()

	cfg := infra.DefaultSQSConfig()
	require.NoError(t, cfg.Validate())
}
