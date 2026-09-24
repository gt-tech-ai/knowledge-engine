// Package schema is the typed-configuration composition seam: it
// composes the concern-grouped config sub-packages (currently schema/infra) into
// a per-service AppConfig, and is the single struct the env-binding derivation
// (viper.deriveEnvBindings) reflects over to bind each field's SEARCH_<PATH> var
// from its mapstructure tag. Wire selects the subset a given service uses; further
// sub-packages (resilience/, stores/, …) sit alongside under this seam.
package schema

import (
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/config/schema/infra"
	listquerycfg "github.com/gt-tech-ai/knowledge-engine/go/foundation/config/schema/listquery"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/config/schema/repos"
)

// StorageConfig groups the storage-tier backends under the "storage" config key.
type StorageConfig struct {
	// S3 is the S3-compatible object storage configuration (storage.s3).
	S3 infra.S3Config `mapstructure:"s3"`
}

// MessagingConfig groups the messaging-tier backends under the "messaging" key.
type MessagingConfig struct {
	// SQS is the SQS-compatible queue configuration (messaging.sqs).
	SQS infra.SQSConfig `mapstructure:"sqs"`
}

// ServerGroup composes the per-service HTTP/RPC server sections under "server".
// Each service reads its own sub-key (server.api, server.identity, server.demo).
type ServerGroup struct {
	// API is the api service's server section (server.api).
	API infra.ServerConfig `mapstructure:"api"`

	// Identity is the identity service's server section (server.identity).
	Identity infra.ServerConfig `mapstructure:"identity"`
}

// AppConfig composes every config section a platform service may read. It is the
// composition seam introduces: the env-binding derivation walks it to
// bind SEARCH_<PATH> for every leaf, and per-service Wire graphs select the
// subset they need. It is NOT unmarshaled wholesale by services today (each reads
// its section via UnmarshalKey); its role is the single source of truth for the
// typed config shape + the derivation's reflection target.
type AppConfig struct {
	// Messaging groups the queue backends (messaging.*).
	Messaging MessagingConfig `mapstructure:"messaging"`

	// App is the application-level section (app.name, app.environment).
	App infra.AppConfig `mapstructure:"app"`

	// Auth is the authentication section (auth.*), including the nested auth0 block.
	Auth infra.AuthConfig `mapstructure:"auth"`

	// Logging is the structured-logging section (logging.*).
	Logging infra.LoggingConfig `mapstructure:"logging"`

	// Storage groups the object-storage backends (storage.*).
	Storage StorageConfig `mapstructure:"storage"`

	// Observability is the tracing/metrics section (observability.*).
	Observability infra.ObservabilityConfig `mapstructure:"observability"`

	// Database is the PostgreSQL section (database.*).
	Database infra.DatabaseConfig `mapstructure:"database"`

	// Redis is the cache section (redis.*).
	Redis infra.RedisConfig `mapstructure:"redis"`

	// Lock is the distributed-lock section (lock.*) — the cross-pod single-active-query lock.
	Lock infra.LockConfig `mapstructure:"lock"`

	// Repos is the data-access section (repos.*), including the query-compiler backend selection
	// (repos.compiler.kind) the list stores use.
	Repos repos.Config `mapstructure:"repos"`

	// Server composes the per-service server sections (server.*).
	Server ServerGroup `mapstructure:"server"`

	// Listquery is the List-Query Platform section (listquery.*) — the value-suggestion
	// facet knobs (row threshold + reconcile interval). It is a pure-value section
	// (no pointers), so it trails the pointer-bearing sections for struct-field alignment.
	Listquery listquerycfg.Config `mapstructure:"listquery"`
}
