package interfaces

// ServiceTokenValidator validates a service-to-service bearer token and returns the
// identity of the calling service. It is the swappable Strategy behind the internal
// service-auth interceptor: Phase 1 ships a static per-caller token
// validator; Phase 2 can swap in a workload-identity validator (K8s SA projected
// tokens / mesh mTLS) without touching the interceptor or its call sites.
type ServiceTokenValidator interface {
	// Validate returns the calling service's name for a valid token, or ok=false when
	// the token matches no known caller.
	Validate(token string) (caller string, ok bool)
}
