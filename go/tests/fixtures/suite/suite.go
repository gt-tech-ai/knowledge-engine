// Package suite provides test suite constructors for unit and integration tests.
//
// It carries the schema-agnostic suites (a gomock UnitSuite plus one suite per
// generic backing service — Redis, MinIO, ElasticMQ). The Postgres/Ent
// IntegrationSuite lives product-side (contracts/ent/tests/fixtures/suite),
// since it provisions the product's Ent schema.
package suite

import (
	"context"

	elasticmqdb "github.com/gt-tech-ai/knowledge-engine/go/tests/fixtures/dbtest/elasticmq"
	miniodb "github.com/gt-tech-ai/knowledge-engine/go/tests/fixtures/dbtest/minio"
	redisdb "github.com/gt-tech-ai/knowledge-engine/go/tests/fixtures/dbtest/redis"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/fixtures/suite/base"
	"go.uber.org/mock/gomock"
)

// UnitSuite provides a test suite for unit tests with a gomock controller.
type UnitSuite struct {
	// Ctrl is the gomock controller, refreshed before each (sub)test.
	Ctrl *gomock.Controller
	// TestSuite is the embedded base suite (assertions, TempDir, IsIntegration).
	base.TestSuite
}

// SetupTest creates a fresh gomock controller for each parent test.
func (s *UnitSuite) SetupTest() {
	s.Ctrl = gomock.NewController(s.T())
}

// SetupSubTest creates a fresh gomock controller for each sub test.
func (s *UnitSuite) SetupSubTest() {
	s.Ctrl = gomock.NewController(s.T())
}

// TearDownTest finishes the gomock controller after each parent test.
func (s *UnitSuite) TearDownTest() {
	s.Ctrl.Finish()
}

// TearDownSubTest finishes the gomock controller after each sub test.
func (s *UnitSuite) TearDownSubTest() {
	s.Ctrl.Finish()
}

// RedisIntegrationSuite provides a test suite with a real Redis instance.
// The RedisAddr field exposes the host:port for the caller to construct its own
// Redis client; no go-redis dependency lives in this package.
type RedisIntegrationSuite struct {
	// redis is the running Redis testcontainer, terminated in TearDownSuite.
	redis *redisdb.TestRedis
	// RedisAddr is the host:port callers use to build a Redis client.
	RedisAddr string
	// TestSuite is the embedded base suite (assertions, TempDir, IsIntegration).
	base.TestSuite
}

// SetupSuite creates a Redis container and waits until it accepts connections.
func (s *RedisIntegrationSuite) SetupSuite() {
	s.TestSuite.SetupSuite()
	s.IsIntegration()

	r, err := redisdb.NewTestRedis(context.Background())
	s.Require().NoError(err, "failed to create test Redis")

	s.redis = r
	s.RedisAddr = r.Addr()
}

// TearDownSuite terminates the Redis container.
func (s *RedisIntegrationSuite) TearDownSuite() {
	if s.redis != nil {
		s.redis.Close(context.Background())
	}
}

// MinIOIntegrationSuite provides a test suite with a real MinIO instance.
//
// The MinIOEndpoint field exposes the S3 API URL for the caller to construct its
// own S3 client; credentials are miniodb.RootUser / miniodb.RootPassword.
type MinIOIntegrationSuite struct {
	// minio is the running MinIO container, terminated in TearDownSuite.
	minio *miniodb.TestMinIO
	// MinIOEndpoint is the S3 API URL callers use to build a client.
	MinIOEndpoint string
	// TestSuite is the embedded base suite (assertions, TempDir, IsIntegration).
	base.TestSuite
}

// SetupSuite creates a MinIO container and waits until it reports healthy.
func (s *MinIOIntegrationSuite) SetupSuite() {
	s.TestSuite.SetupSuite()
	s.IsIntegration()

	m, err := miniodb.NewTestMinIO(context.Background())
	s.Require().NoError(err, "failed to create test MinIO")

	s.minio = m
	s.MinIOEndpoint = m.Endpoint()
}

// TearDownSuite terminates the MinIO container.
func (s *MinIOIntegrationSuite) TearDownSuite() {
	if s.minio != nil {
		s.minio.Close(context.Background())
	}
}

// ElasticMQIntegrationSuite provides a test suite with a real ElasticMQ instance.
//
// The ElasticMQEndpoint field exposes the SQS-compatible API URL for the caller to
// construct its own AWS-SDK SQS client; credentials are elasticmqdb.AccessKey /
// elasticmqdb.SecretKey.
type ElasticMQIntegrationSuite struct {
	// elasticmq is the running ElasticMQ container, terminated in TearDownSuite.
	elasticmq *elasticmqdb.TestElasticMQ
	// ElasticMQEndpoint is the SQS API URL callers use to build a client.
	ElasticMQEndpoint string
	// TestSuite is the embedded base suite (assertions, TempDir, IsIntegration).
	base.TestSuite
}

// SetupSuite creates an ElasticMQ container and waits until it accepts connections.
func (s *ElasticMQIntegrationSuite) SetupSuite() {
	s.TestSuite.SetupSuite()
	s.IsIntegration()

	e, err := elasticmqdb.NewTestElasticMQ(context.Background())
	s.Require().NoError(err, "failed to create test ElasticMQ")

	s.elasticmq = e
	s.ElasticMQEndpoint = e.Endpoint()
}

// TearDownSuite terminates the ElasticMQ container.
func (s *ElasticMQIntegrationSuite) TearDownSuite() {
	if s.elasticmq != nil {
		s.elasticmq.Close(context.Background())
	}
}
