package viper

import (
	"reflect"
	"strings"

	"github.com/spf13/viper"
)

// envReplacer maps a mapstructure dotted path to the SEARCH_<PATH> env-var form
// (dots and dashes → underscores), matching the loader's SetEnvKeyReplacer.
var envReplacer = strings.NewReplacer(".", "_", "-", "_")

// deriveEnvBindings binds every leaf field of cfg to its env vars, derived from
// the field's mapstructure tag path: the canonical SEARCH_<PATH> name plus any
// aliases listed in an `envalias:"A,B"` tag (legacy flat names like DB_HOST). It
// replaces the hand-maintained bindLegacyEnvVars for struct-backed config: adding
// a field auto-binds its env var, and an alias is a tag, not a code edit
// cfg must be a pointer to a struct.
func deriveEnvBindings(v *viper.Viper, cfg any) {
	walkEnvBindings(v, reflect.TypeOf(cfg), "")
}

// walkEnvBindings recurses cfg's struct fields, accumulating the mapstructure
// path prefix, and binds each scalar leaf. Nested structs recurse; scalars
// (including time.Duration, whose Kind is Int64) are leaves.
func walkEnvBindings(v *viper.Viper, t reflect.Type, prefix string) {
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
			walkEnvBindings(v, ft, path)
			continue
		}

		// Leaf: bind the canonical SEARCH_<PATH> plus any envalias aliases. Passing
		// explicit names disables Viper's auto-prefix, so SEARCH_<PATH> is included
		// explicitly to preserve the prefixed override.
		names := []string{path, "SEARCH_" + strings.ToUpper(envReplacer.Replace(path))}
		if alias := f.Tag.Get("envalias"); alias != "" {
			names = append(names, strings.Split(alias, ",")...)
		}
		_ = v.BindEnv(names...)
	}
}
