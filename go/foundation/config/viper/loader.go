// Package viper provides a Viper-backed ConfigLoader implementation.
//
// It reads a hierarchical YAML config:
// base.yaml (base) -> {env}.yaml (overlay) -> secrets.yaml -> env vars.
package viper

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	coreerr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/config/schema"
	"github.com/spf13/viper"
)

// Compile-time interface assertion.
var _ interfaces.ConfigLoader = (*Loader)(nil)

// Config holds Viper-specific configuration for the Loader.
type Config struct {
	// BaseDir is the directory containing base.yaml, {env}.yaml overlays, and secrets.yaml.
	BaseDir string

	// Env is the environment name (e.g., "dev", "staging", "prod") for overlay selection.
	Env string

	// Prefix is the environment variable prefix for automatic binding (default: "SEARCH").
	Prefix string
}

// DefaultConfig returns sensible defaults for a Viper loader.
func DefaultConfig() Config {
	return Config{
		BaseDir: ".",
		Env:     ResolveEnv(),
		Prefix:  "SEARCH",
	}
}

// Loader wraps a *viper.Viper instance and implements interfaces.ConfigLoader.
// Extended methods (Load, Viper) are available on the concrete type only.
type Loader struct {
	// v is the underlying Viper instance holding the merged configuration.
	v *viper.Viper
	// baseDir is the directory holding base.yaml, {env}.yaml, and secrets.yaml.
	baseDir string
	// env is the environment name selecting the {env}.yaml overlay.
	env string
	// prefix is the environment-variable prefix for automatic binding.
	prefix string
}

// New creates a new Viper-backed Loader from the given Config.
// Call Load() to read the configuration hierarchy before using getter methods.
func New(cfg Config) *Loader {
	return &Loader{
		v:       viper.New(),
		baseDir: cfg.BaseDir,
		env:     cfg.Env,
		prefix:  cfg.Prefix,
	}
}

// Load reads the configuration hierarchy into the underlying Viper instance.
// Layer order: base.yaml -> {env}.yaml -> secrets.yaml -> env vars.
// This is an extended method available only on the concrete *Loader type.
func (l *Loader) Load() error {
	l.v.SetConfigType("yaml")
	l.v.AutomaticEnv()
	l.v.SetEnvPrefix(l.prefix)
	l.v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))

	// Layer 1: Base config
	l.v.SetConfigFile(fmt.Sprintf("%s/base.yaml", l.baseDir))
	if err := l.v.ReadInConfig(); err != nil {
		if !os.IsNotExist(err) {
			return coreerr.Wrap(err, coreerr.CodeInternal, "read base config")
		}
	}

	// Layer 2: Environment overlay
	if l.env != "" {
		envFile := fmt.Sprintf("%s/%s.yaml", l.baseDir, l.env)
		if _, err := os.Stat(envFile); err == nil {
			l.v.SetConfigFile(envFile)
			if err := l.v.MergeInConfig(); err != nil {
				return coreerr.Wrap(
					err,
					coreerr.CodeInternal,
					fmt.Sprintf("merge env config %s", l.env),
				)
			}
		}
	}

	// Layer 3: Secrets file
	secretsFile := fmt.Sprintf("%s/secrets.yaml", l.baseDir)
	if _, err := os.Stat(secretsFile); err == nil {
		l.v.SetConfigFile(secretsFile)
		if err := l.v.MergeInConfig(); err != nil {
			return coreerr.Wrap(err, coreerr.CodeInternal, "merge secrets")
		}
	}

	// Layer 4: Env bindings. The struct-backed config sections bind their
	// SEARCH_<PATH> + legacy-alias env vars automatically, derived from the
	// schema.AppConfig mapstructure/envalias tags. The
	// non-struct keys (read via GetString, not unmarshaled into a schema struct)
	// are bound explicitly below.
	deriveEnvBindings(l.v, &schema.AppConfig{})
	l.bindNonSchemaEnv()

	return nil
}

// bindNonSchemaEnv binds the env vars for keys that are NOT fields of the
// schema.AppConfig structs — they are read directly via GetString rather than
// unmarshaled — so the tag-derived deriveEnvBindings cannot cover them. This is
// the small, honest remnant of the former bindLegacyEnvVars: the sqs backend
// selector, the api→identity resolution target, the flat auth.auth0_* client
// keys, and the seed job's run knobs.
func (l *Loader) bindNonSchemaEnv() {
	// Messaging backend selector — SQSConfig has no Kind field (the messaging
	// tier reads messaging.sqs.kind via GetString), so it is not tag-derivable.
	_ = l.v.BindEnv("messaging.sqs.kind", "SEARCH_MESSAGING_SQS_KIND", "SQS_KIND")

	// Identity resolution target: the api dials the identity service for
	// per-request user-context resolution. ServerConfig has no identity_target
	// field (read via GetString), so bind the in-cluster target explicitly — the
	// Docker/K8s override (IDENTITY_GRPC_TARGET=identity:8090) must be honored
	// instead of the base.yaml default, or every workspace/authz call fails with
	// "resolve identity: connection refused".
	_ = l.v.BindEnv(
		"server.api.identity_target",
		"SEARCH_SERVER_API_IDENTITY_TARGET",
		"IDENTITY_GRPC_TARGET",
	)

	// Flat auth.auth0_* client keys (distinct from the nested auth.auth0.* JWT
	// block, which IS tag-derived). The identity Auth0-client provider reads these
	// flat keys via GetString; the deployed service gets them from env/secrets, not
	// YAML, so an unbound key fails startup with "auth.auth0_client_id not set".
	_ = l.v.BindEnv("auth.auth0_domain", "AUTH0_DOMAIN")
	_ = l.v.BindEnv("auth.auth0_client_id", "AUTH0_CLIENT_ID")
	_ = l.v.BindEnv("auth.auth0_management_api_id", "AUTH0_MANAGEMENT_API_CLIENT_ID")
	_ = l.v.BindEnv(
		"auth.auth0_management_api_secret",
		"AUTH0_MANAGEMENT_API_CLIENT_SECRET",
	)

	// Seed: the env overlay sets seed.mode; these allow per-run overrides (a seeding
	// tool setting SEED_MODE) and CI/deploy injection.
	_ = l.v.BindEnv("seed.mode", "SEED_MODE")
	_ = l.v.BindEnv("seed.org_external_id", "SEED_ORG_EXTERNAL_ID")
	_ = l.v.BindEnv("seed.org_name", "SEED_ORG_NAME")
	_ = l.v.BindEnv("seed.org_slug", "SEED_ORG_SLUG")
	_ = l.v.BindEnv("seed.allow_destructive", "SEED_ALLOW_DESTRUCTIVE")
	// Initial persona password (staging injects it from ESO as SEED_PERSONA_PASSWORD;
	// no committed default reaches a deployed tenant). AutomaticEnv applies the SEARCH_
	// prefix, so the unprefixed name needs an explicit bind like the other seed keys.
	_ = l.v.BindEnv("seed.persona_password", "SEED_PERSONA_PASSWORD")
	// Per-org app-layer row counts (defaults live in the seed job when unset).
	_ = l.v.BindEnv("seed.volume.teams", "SEED_VOLUME_TEAMS")
	_ = l.v.BindEnv("seed.volume.workspaces", "SEED_VOLUME_WORKSPACES")
	_ = l.v.BindEnv("seed.volume.docs_per_workspace", "SEED_VOLUME_DOCS_PER_WORKSPACE")
	_ = l.v.BindEnv("seed.volume.connectors", "SEED_VOLUME_CONNECTORS")
	_ = l.v.BindEnv(
		"seed.volume.turns_per_conversation",
		"SEED_VOLUME_TURNS_PER_CONVERSATION",
	)
}

// Get returns the raw value for a key, or nil if not set.
func (l *Loader) Get(key string) any {
	return l.v.Get(key)
}

// GetString returns the string value for a key.
func (l *Loader) GetString(key string) string {
	return l.v.GetString(key)
}

// GetInt returns the int value for a key.
func (l *Loader) GetInt(key string) int {
	return l.v.GetInt(key)
}

// GetBool returns the bool value for a key.
func (l *Loader) GetBool(key string) bool {
	return l.v.GetBool(key)
}

// Unmarshal decodes the full configuration into the target struct.
func (l *Loader) Unmarshal(target any) error {
	return l.v.Unmarshal(target)
}

// UnmarshalKey decodes a specific configuration section into the target struct.
// It builds a resolved nested map by calling Get() for each sub-key, which
// correctly resolves BindEnv() and AutomaticEnv() bindings that Viper's
// built-in UnmarshalKey ignores for nested keys.
func (l *Loader) UnmarshalKey(key string, target any) error {
	if !l.v.IsSet(key) {
		return coreerr.New(
			coreerr.CodeNotFound,
			fmt.Sprintf("config key %q not found", key),
		)
	}

	// Build a resolved nested map: iterate all sub-keys under the prefix and
	// call Get() on each (which respects BindEnv and AutomaticEnv bindings),
	// then expand dotted sub-keys into nested maps for mapstructure.
	resolved := make(map[string]any)
	prefix := key + "."
	for _, k := range l.v.AllKeys() {
		if strings.HasPrefix(k, prefix) {
			subKey := k[len(prefix):]
			setNested(resolved, subKey, l.v.Get(k))
		}
	}

	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
			StringToStringMapHookFunc(),
		),
		TagName:          "mapstructure",
		Result:           target,
		WeaklyTypedInput: true,
	})
	if err != nil {
		return coreerr.Wrap(err, coreerr.CodeInternal, "create decoder")
	}

	return decoder.Decode(resolved)
}

// setNested expands a dotted key (e.g., "identity.port") into nested maps.
func setNested(m map[string]any, key string, value any) {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) == 1 {
		m[key] = value
		return
	}
	sub, ok := m[parts[0]].(map[string]any)
	if !ok {
		sub = make(map[string]any)
		m[parts[0]] = sub
	}
	setNested(sub, parts[1], value)
}

// Viper returns the underlying *viper.Viper instance for advanced usage.
// This is an escape hatch available only on the concrete *Loader type.
func (l *Loader) Viper() *viper.Viper {
	return l.v
}
