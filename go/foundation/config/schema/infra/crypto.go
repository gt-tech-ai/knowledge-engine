package infra

import (
	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// CryptoConfig carries the application's data-at-rest encryption key (connector
// credentials, …). The key is base64-encoded (32 bytes decoded), sourced from configuration — a dev
// default in base.yaml, overridden by secrets.yaml / an ESO externalsecret in staging/prod.
type CryptoConfig struct {
	// ConnectorCredentialsKey is the base64 AES-256 key used to encrypt connector auth configs.
	ConnectorCredentialsKey string `mapstructure:"connector_credentials_key"`
}

// DefaultCryptoConfig returns an empty config; the key MUST be supplied by configuration (there is no
// hardcoded key), so Validate fails loudly when it is absent.
func DefaultCryptoConfig() CryptoConfig {
	return CryptoConfig{}
}

// Validate rejects a missing connector-credentials key (the crypto module enforces the length).
func (c CryptoConfig) Validate() error {
	if c.ConnectorCredentialsKey == "" {
		return apperr.InvalidInput("crypto.connector_credentials_key is required")
	}
	return nil
}
