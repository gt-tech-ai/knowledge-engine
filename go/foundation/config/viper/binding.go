package viper

import (
	"reflect"
	"strings"

	"github.com/spf13/viper"
)

// envReplacer maps a mapstructure dotted path to the <PREFIX>_<PATH> env-var form
// (dots and dashes → underscores), matching the loader's SetEnvKeyReplacer.
var envReplacer = strings.NewReplacer(".", "_", "-", "_")

// deriveEnvBindings binds every leaf field of cfg to its env vars, derived from
// the field's mapstructure tag path: the canonical <envPrefix>_<PATH> name (plain
// <PATH> when envPrefix is empty) plus any aliases listed in an `envalias:"A,B"`
// tag (flat names like DB_HOST). Adding a field auto-binds its env var, and an
// alias is a tag, not a code edit. cfg must be a struct or a pointer to one.
func deriveEnvBindings(v *viper.Viper, cfg any, envPrefix string) {
	walkEnvBindings(v, reflect.TypeOf(cfg), "", envPrefix)
}

// walkEnvBindings recurses cfg's struct fields, accumulating the mapstructure
// path prefix, and binds each scalar leaf. Nested structs recurse; scalars
// (including time.Duration, whose Kind is Int64) are leaves.
func walkEnvBindings(v *viper.Viper, t reflect.Type, prefix, envPrefix string) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("mapstructure")
		if tag == "" || tag == "-" {
			continue
		}
		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}

		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			walkEnvBindings(v, ft, path, envPrefix)
			continue
		}

		// Leaf: bind the canonical <envPrefix>_<PATH> plus any envalias aliases.
		// Passing explicit names disables Viper's auto-prefix, so the prefixed name
		// is included explicitly to preserve the prefixed override.
		envName := strings.ToUpper(envReplacer.Replace(path))
		if envPrefix != "" {
			envName = strings.ToUpper(envPrefix) + "_" + envName
		}
		names := []string{path, envName}
		if alias := f.Tag.Get("envalias"); alias != "" {
			names = append(names, strings.Split(alias, ",")...)
		}
		_ = v.BindEnv(names...)
	}
}
