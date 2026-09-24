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
	"github.com/spf13/viper"
)

// Compile-time interface assertion.
var _ interfaces.ConfigLoader = (*Loader)(nil)

// Config holds Viper-specific configuration for the Loader.
type Config struct {
	// Schema is the consumer's root config struct (or a pointer to one). Every leaf
	// field is bound to <Prefix>_<PATH> plus its envalias names. Nil derives no
	// bindings (AutomaticEnv still maps <Prefix>_<KEY> for keys present in the YAML).
	Schema any

	// ExtraEnv binds config keys that are not fields of Schema to env var names
	// (key → names, first set wins). A key listed here replaces any binding Schema
	// derived for it.
	ExtraEnv map[string][]string

	// BaseDir is the directory containing base.yaml, {env}.yaml overlays, and secrets.yaml.
	BaseDir string

	// Env is the environment name (e.g., "dev", "staging", "prod") for overlay selection.
	Env string

	// Prefix is the environment variable prefix for automatic binding; empty means
	// unprefixed names (a consumer normally sets its own, e.g. "MYAPP").
	Prefix string
}

// DefaultConfig returns sensible defaults for a Viper loader.
func DefaultConfig() Config {
	return Config{
		BaseDir: ".",
		Env:     ResolveEnv(),
	}
}

// Loader wraps a *viper.Viper instance and implements interfaces.ConfigLoader.
// Extended methods (Load, Viper) are available on the concrete type only.
type Loader struct {
	// v is the underlying Viper instance holding the merged configuration.
	v *viper.Viper
	// schema is the root config struct whose leaf fields get derived env bindings.
	schema any
	// extraEnv holds the consumer's explicit key → env-var-names bindings.
	extraEnv map[string][]string
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
		v:        viper.New(),
		baseDir:  cfg.BaseDir,
		env:      cfg.Env,
		prefix:   cfg.Prefix,
		schema:   cfg.Schema,
		extraEnv: cfg.ExtraEnv,
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

	// Layer 4: Env bindings. The consumer's schema binds <PREFIX>_<PATH> + alias env
	// vars for every leaf, derived from its mapstructure/envalias tags; the consumer's
	// ExtraEnv (keys read via GetString, not unmarshaled into the schema) applies last.
	if l.schema != nil {
		deriveEnvBindings(l.v, l.schema, l.prefix)
	}
	for key, names := range l.extraEnv {
		_ = l.v.BindEnv(append([]string{key}, names...)...)
	}

	return nil
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
