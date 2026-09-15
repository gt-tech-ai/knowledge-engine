package viper

import (
	"os"
	"strings"
)

// envSelectors are the environment variables consulted, in order, to determine
// which {env}.yaml overlay to load. SEARCH_ENV is the canonical selector;
// APP_ENV and ENVIRONMENT are the names container orchestration (the service
// Helm charts and the environment-config ConfigMap) already set, so a
// deployment that declares its environment through any of them selects the
// matching overlay rather than silently falling back to base.yaml.
var envSelectors = []string{"SEARCH_ENV", "APP_ENV", "ENVIRONMENT"}

// ResolveEnv returns the deployment environment name for overlay selection.
// It prefers SEARCH_ENV and falls back to APP_ENV then ENVIRONMENT, returning
// the first non-empty value lowercased (overlay files are dev/staging/prod).
// It returns "" when none are set, which makes the loader skip the overlay and
// use base.yaml alone (the local-development default).
func ResolveEnv() string {
	for _, key := range envSelectors {
		if v := strings.ToLower(strings.TrimSpace(os.Getenv(key))); v != "" {
			return v
		}
	}
	return ""
}
