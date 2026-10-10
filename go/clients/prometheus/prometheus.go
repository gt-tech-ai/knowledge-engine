// Package prometheus is the client for a Prometheus server's HTTP query API: instant
// queries reduced to one sample, and range queries reduced to one series. Both fail
// closed: a result that is not exactly one finite value (or one series of them) is an
// error, never a silent zero. NewFromConfig picks the backend: the zero-infrastructure
// stub (the default) or the HTTP client, its transport wrapped in the client stack.
package prometheus

import (
	"fmt"
	"net/http"
	"time"

	clientdecorators "github.com/gt-tech-ai/knowledge-engine/go/clients/decorators"
	"github.com/gt-tech-ai/knowledge-engine/go/clients/prometheus/stub"
	coreerr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
)

// Kind selects the metrics-query backend.
type Kind int

const (
	// KindStub answers every query with zero or an empty series (the default).
	KindStub Kind = iota
	// KindHTTP queries a Prometheus server's HTTP API.
	KindHTTP
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindStub:
		return "stub"
	case KindHTTP:
		return "http"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// Config selects and configures the metrics querier.
type Config struct {
	// BaseURL is the Prometheus server root (e.g. its in-cluster service address);
	// required for KindHTTP.
	BaseURL string `yaml:"base_url" mapstructure:"base_url"`

	// Resilience tunes the client stack around each HTTP request.
	Resilience clientdecorators.Config `yaml:"resilience" mapstructure:"resilience"`

	// Timeout is the http.Client timeout: it bounds each HTTP attempt, response body
	// included, even with the client stack disabled; zero means no transport bound.
	Timeout time.Duration `yaml:"timeout" mapstructure:"timeout"`

	// Kind selects the backend.
	Kind Kind `yaml:"kind" mapstructure:"kind"`
}

// DefaultConfig returns the stub kind, a 30-second request timeout and the default
// client stack.
func DefaultConfig() Config {
	return Config{
		Kind:       KindStub,
		Timeout:    30 * time.Second,
		Resilience: clientdecorators.DefaultConfig(),
	}
}

// NewFromConfig builds the querier cfg selects, without I/O. The HTTP kind sends
// through an http.Client bounded by cfg.Timeout and wrapped in the client stack built
// from cfg.Resilience and deps. An unknown kind, or KindHTTP without a BaseURL, is
// CodeInvalidInput.
func NewFromConfig(cfg Config, deps clientdecorators.Deps) (interfaces.MetricsQuerier, error) {
	switch cfg.Kind {
	case KindStub:
		return stub.New(), nil
	case KindHTTP:
		if cfg.BaseURL == "" {
			return nil, coreerr.New(coreerr.CodeInvalidInput, "prometheus: base_url is required for the http kind")
		}
		stack, err := clientdecorators.StackFromConfig("prometheus", cfg.Resilience, deps)
		if err != nil {
			return nil, err
		}
		doer := DecorateDoer(&http.Client{Timeout: cfg.Timeout}, stack)
		return New(cfg.BaseURL, doer), nil
	default:
		return nil, coreerr.New(coreerr.CodeInvalidInput, fmt.Sprintf("unknown prometheus kind: %v", cfg.Kind))
	}
}
