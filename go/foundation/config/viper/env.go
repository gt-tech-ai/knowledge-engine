package viper

import (
	"os"
	"strings"
)

// envSelectors are the environment variables ResolveEnv consults, in order, to
// determine which {env}.yaml overlay to load: APP_ENV, then ENVIRONMENT. A consumer
// with its own selector (e.g. MYAPP_ENV) calls ResolveEnvFrom instead.
var envSelectors = []string{"APP_ENV", "ENVIRONMENT"}

// ResolveEnv returns the deployment environment name for overlay selection from
// APP_ENV, falling back to ENVIRONMENT; see ResolveEnvFrom.
func ResolveEnv() string {
	return ResolveEnvFrom(envSelectors...)
}

// ResolveEnvFrom returns the first non-empty value among the given environment
// variables, trimmed and lowercased (overlay files are dev/staging/prod), so a
// consumer chooses which variables declare its environment. It returns "" when
// none are set, which makes the loader skip the overlay and use base.yaml alone
// (the local-development default).
func ResolveEnvFrom(selectors ...string) string {
	for _, key := range selectors {
		if v := strings.ToLower(strings.TrimSpace(os.Getenv(key))); v != "" {
			return v
		}
	}
	return ""
}
