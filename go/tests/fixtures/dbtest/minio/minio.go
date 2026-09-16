// Package minio provides a testcontainers-based MinIO instance for integration
// tests. It mirrors the redis sibling package: start a container, wait until it
// is healthy, and expose the endpoint + static credentials so the caller can
// build any S3 client it needs. No AWS SDK dependency lives in this package.
package minio

import (
	"context"
	"fmt"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	coreerr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

const (
	// minioImage is the pinned MinIO container image (matches the dev Compose stack).
	minioImage = "minio/minio:latest"
	// minioAPIPort is the container's S3 API port.
	minioAPIPort = "9000/tcp"

	// RootUser is the static access key the container is started with.
	RootUser = "minioadmin"
	// RootPassword is the static secret key the container is started with.
	RootPassword = "minioadmin"
)

// TestMinIO wraps a testcontainers MinIO instance. Callers receive the endpoint
// URL and use RootUser/RootPassword with their own S3 client to connect.
type TestMinIO struct {
	// container is the running MinIO testcontainer.
	container testcontainers.Container
	// endpoint is the resolved http://host:port S3 API URL.
	endpoint string
}

// NewTestMinIO starts a MinIO container and waits up to 60 seconds for its
// readiness endpoint to report healthy.
func NewTestMinIO(ctx context.Context) (*TestMinIO, error) {
	req := testcontainers.ContainerRequest{
		Image:        minioImage,
		ExposedPorts: []string{minioAPIPort},
		Env: map[string]string{
			"MINIO_ROOT_USER":     RootUser,
			"MINIO_ROOT_PASSWORD": RootPassword,
		},
		Cmd: []string{"server", "/data"},
		WaitingFor: wait.ForHTTP("/minio/health/ready").
			WithPort(minioAPIPort).
			WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(
		ctx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		},
	)
	if err != nil {
		return nil, coreerr.Wrap(
			err,
			coreerr.CodeInternal,
			"failed to start MinIO container",
		)
	}

	host, err := container.Host(ctx)
	if err != nil {
		container.Terminate(ctx) //nolint:errcheck // best-effort cleanup on setup failure
		return nil, coreerr.Wrap(
			err,
			coreerr.CodeInternal,
			"failed to get container host",
		)
	}

	port, err := container.MappedPort(ctx, minioAPIPort)
	if err != nil {
		container.Terminate(ctx) //nolint:errcheck // best-effort cleanup on setup failure
		return nil, coreerr.Wrap(err, coreerr.CodeInternal, "failed to get mapped port")
	}

	return &TestMinIO{
		container: container,
		endpoint:  fmt.Sprintf("http://%s:%s", host, port.Port()),
	}, nil
}

// Endpoint returns the MinIO S3 API endpoint URL (http://host:port).
func (m *TestMinIO) Endpoint() string {
	return m.endpoint
}

// Close terminates the MinIO container. Safe to call on a nil receiver.
func (m *TestMinIO) Close(ctx context.Context) {
	if m != nil && m.container != nil {
		m.container.Terminate(ctx) //nolint:errcheck // best-effort cleanup on teardown
	}
}
