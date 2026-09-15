// Package transport is the config surface for the edge (transport) layer
// WebSocket/gRPC/CORS/HTTP tuning, the rate-limiter
// toggle that finally activates the token limiter, interceptor toggles, and the
// identity-client timeouts. Defaults preserve today's behavior (the rate limiter
// stays disabled; CORS stays permissive), so adopting the surface is
// behavior-preserving. Pure data (mirrors infra/S3Config); the providers convert
// it into the server/middleware wiring.
package transport

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// WSConfig tunes the WebSocket server. idle/write/ping had no bound today; these
// add sensible defaults a deployer can tune.
type WSConfig struct {
	// AllowedOrigins lists the request Origins accepted on the WebSocket handshake
	// when AllowInsecureOrigins is false. Behind a gateway (staging/prod) the
	// browser's Origin (e.g. staging.knowledge-engine.com) differs from the upstream Host
	// (api.app.svc.cluster.local:8092), so the coder/websocket default
	// Origin==Host check rejects every upgrade with 403; listing the frontend
	// origin(s) here passes them through as the Accept OriginPatterns. Empty keeps
	// the strict same-origin default. Patterns may include a wildcard subdomain
	// (e.g. "*.knowledge-engine.com").
	AllowedOrigins []string `mapstructure:"allowed_origins"`

	// IdleTimeout closes a connection idle for longer than this (0 = no timeout).
	IdleTimeout time.Duration `mapstructure:"idle_timeout"`

	// WriteDeadline bounds a single message write (0 = no deadline).
	WriteDeadline time.Duration `mapstructure:"write_deadline"`

	// PingInterval is the server keepalive ping cadence (0 = disabled).
	PingInterval time.Duration `mapstructure:"ping_interval"`

	// PongTimeout bounds how long a heartbeat ping waits for its pong before the socket is
	// declared dead and closed. 0 = wait forever (heartbeat effectively disabled).
	PongTimeout time.Duration `mapstructure:"pong_timeout"`

	// MaxMessageBytes caps an inbound WS message size; the WS server threads it into
	// SetReadLimit, so an oversize frame triggers a 1009 close (0 = library default).
	MaxMessageBytes int64 `mapstructure:"max_message_bytes"`

	// MaxConnections caps the total concurrent WebSocket connections the hub accepts
	// on this machine (0 = unlimited).
	MaxConnections int `mapstructure:"max_connections"`

	// MaxConnectionsPerUser caps concurrent connections owned by a single user
	// (0 = unlimited).
	MaxConnectionsPerUser int `mapstructure:"max_connections_per_user"`

	// AllowInsecureOrigins disables the handshake Origin check (local dev only);
	// replaces the ad-hoc os.Getenv("APP_ENV") devMode (coordinates with 10.9).
	AllowInsecureOrigins bool `mapstructure:"allow_insecure_origins"`
}

// GRPCConfig tunes the gRPC/Connect client + server keepalive and message size.
type GRPCConfig struct {
	// KeepaliveTime is the client keepalive ping cadence (0 = library default).
	KeepaliveTime time.Duration `mapstructure:"keepalive_time"`

	// KeepaliveTimeout is how long a keepalive ping waits for an ack.
	KeepaliveTimeout time.Duration `mapstructure:"keepalive_timeout"`

	// MaxMessageBytes caps a gRPC message size (0 = library default).
	MaxMessageBytes int64 `mapstructure:"max_message_bytes"`
}

// CORSConfig tunes cross-origin sharing. Defaults are permissive (allow all) to
// preserve today's behavior; the prod overlay restricts allow_origins.
type CORSConfig struct {
	// MaxAge is how long (seconds) browsers may cache a preflight result.
	MaxAge string `mapstructure:"max_age"`

	// AllowOrigins lists permitted origins; ["*"] allows all (today's default).
	AllowOrigins []string `mapstructure:"allow_origins"`
}

// RateLimitConfig toggles + tunes the token rate limiter, which is unwired today.
// Enabled defaults false so activation is opt-in and behavior-preserving.
type RateLimitConfig struct {
	// Rate is the sustained requests per second when enabled.
	Rate float64 `mapstructure:"rate"`

	// Burst is the maximum request burst when enabled.
	Burst int `mapstructure:"burst"`

	// Enabled activates the rate limiter (default false — today's behavior).
	Enabled bool `mapstructure:"enabled"`
}

// IdentityClientConfig tunes the api→identity gRPC client.
type IdentityClientConfig struct {
	// Timeout bounds an identity RPC (was the hardcoded 5s).
	Timeout time.Duration `mapstructure:"timeout"`

	// CacheTTL is the resolved-context cache lifetime (0 = no client cache).
	CacheTTL time.Duration `mapstructure:"cache_ttl"`
}

// Config is the edge-layer config surface.
type Config struct {
	// CORS tunes cross-origin sharing.
	CORS CORSConfig `mapstructure:"cors"`

	// WS tunes the WebSocket server.
	WS WSConfig `mapstructure:"ws"`

	// GRPC tunes the gRPC/Connect keepalive + message size.
	GRPC GRPCConfig `mapstructure:"grpc"`

	// RateLimit toggles + tunes the token rate limiter.
	RateLimit RateLimitConfig `mapstructure:"rate_limit"`

	// IdentityClient tunes the api→identity gRPC client.
	IdentityClient IdentityClientConfig `mapstructure:"identity_client"`

	// MaxHeaderBytes caps the HTTP request header size (was Go's 1 MiB default).
	MaxHeaderBytes int `mapstructure:"max_header_bytes"`
}

// DefaultConfig returns behavior-preserving defaults: the rate limiter disabled,
// CORS permissive, the identity timeout at today's 5s, and sensible bounds for
// the previously-unbounded WS/HTTP knobs.
func DefaultConfig() Config {
	return Config{
		WS: WSConfig{
			// 5m matches the api WS server's longer idle for persistent connections.
			IdleTimeout:   5 * time.Minute,
			WriteDeadline: 10 * time.Second,
			PingInterval:  30 * time.Second,
			PongTimeout:   10 * time.Second,
			// 64 KiB is the WS read limit: an oversize inbound frame triggers a
			// 1009 close. The WS server threads this into SetReadLimit (it is the WS-specific
			// message cap, not the larger generic HTTP body limit).
			MaxMessageBytes:       64 << 10,
			MaxConnections:        10000,
			MaxConnectionsPerUser: 20,
		},
		GRPC: GRPCConfig{
			KeepaliveTime:    30 * time.Second,
			KeepaliveTimeout: 10 * time.Second,
			MaxMessageBytes:  4 << 20,
		},
		CORS: CORSConfig{
			AllowOrigins: []string{"*"},
			MaxAge:       "3600",
		},
		RateLimit: RateLimitConfig{
			Enabled: false,
			Rate:    100,
			Burst:   10,
		},
		IdentityClient: IdentityClientConfig{
			Timeout:  5 * time.Second,
			CacheTTL: 0,
		},
		MaxHeaderBytes: 1 << 20,
	}
}

// Validate rejects negative durations/sizes and an enabled-but-zero rate limit.
func (c Config) Validate() error {
	if c.WS.IdleTimeout < 0 || c.WS.WriteDeadline < 0 || c.WS.PingInterval < 0 ||
		c.WS.PongTimeout < 0 {
		return apperr.InvalidInput("transport.ws timeouts must be >= 0")
	}
	if c.WS.MaxConnections < 0 || c.WS.MaxConnectionsPerUser < 0 {
		return apperr.InvalidInput("transport.ws connection limits must be >= 0")
	}
	if c.RateLimit.Enabled && c.RateLimit.Rate <= 0 {
		return apperr.InvalidInput("transport.rate_limit.rate must be > 0 when enabled")
	}
	if c.RateLimit.Enabled && c.RateLimit.Burst <= 0 {
		return apperr.InvalidInput("transport.rate_limit.burst must be > 0 when enabled")
	}
	if c.MaxHeaderBytes < 0 {
		return apperr.InvalidInput("transport.max_header_bytes must be >= 0")
	}
	return nil
}
