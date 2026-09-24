package config

import (
	viperloader "github.com/gt-tech-ai/knowledge-engine/go/foundation/config/viper"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/options"
)

// Config is the top-level configuration for the config loader factory.
type Config struct {
	// Viper holds Viper-specific configuration.
	Viper ViperConfig

	// Kind specifies which config loader implementation to use.
	Kind Kind
}

// ViperConfig holds Viper-specific configuration. This is a subset of
// viper.Config, adapted for the parent config package.
type ViperConfig struct {
	// Schema is the consumer's root config struct whose leaf fields get derived
	// env bindings; nil uses the built-in schema.AppConfig.
	Schema any

	// ExtraEnv binds config keys outside Schema to env var names (key → names).
	ExtraEnv map[string][]string

	// BaseDir is the directory containing base.yaml, {env}.yaml overlays, and secrets.yaml.
	BaseDir string

	// Env is the environment name (e.g., "dev", "staging", "prod") for overlay selection.
	Env string

	// Prefix is the environment variable prefix for automatic binding (default: "SEARCH").
	Prefix string
}

// DefaultConfig returns the default config with KindViper and sensible defaults.
func DefaultConfig() Config {
	return Config{
		Kind: KindViper,
		Viper: ViperConfig{
			BaseDir: ".",
			Env:     viperloader.ResolveEnv(),
			Prefix:  "SEARCH",
		},
	}
}

// ToOptions converts this Config to a slice of Option functions.
func (c Config) ToOptions() []options.Option[Config] {
	return []options.Option[Config]{
		func(target *Config) { *target = c },
	}
}

// WithBaseDir sets the directory containing config files.
func WithBaseDir(dir string) options.Option[Config] {
	return func(c *Config) { c.Viper.BaseDir = dir }
}

// WithEnvironment sets the environment name for overlay loading.
func WithEnvironment(env string) options.Option[Config] {
	return func(c *Config) { c.Viper.Env = env }
}

// WithEnvPrefix sets the prefix for environment variable binding (default: "SEARCH").
func WithEnvPrefix(prefix string) options.Option[Config] {
	return func(c *Config) { c.Viper.Prefix = prefix }
}

// WithSchema sets the consumer's root config struct used to derive env bindings.
func WithSchema(root any) options.Option[Config] {
	return func(c *Config) { c.Viper.Schema = root }
}

// WithExtraEnv binds config keys outside the schema to env var names (key → names).
func WithExtraEnv(bindings map[string][]string) options.Option[Config] {
	return func(c *Config) { c.Viper.ExtraEnv = bindings }
}
