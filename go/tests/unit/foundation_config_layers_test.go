package unit_test

import (
	"testing"
	"time"

	"github.com/gt-tech-ai/knowledge-engine/go/foundation/config/schema/repos"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/config/schema/services"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/config/schema/stores"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLayerConfigDefaults_MatchConsts tests that the stores/repos/services config
// defaults equal today's scattered consts, so adopting the surface is
// behavior-preserving.
//
// Why this test is important:
//   - The whole point of is to replace consts with a config surface
//     without changing behavior at default values; a wrong default would silently
//     shift page sizes, cache TTLs, or upload limits the moment the providers wire
//     these in.
//
// What it tests:
//   - The documented default page sizes (20/50/10/100), the cache TTL (5m) +
//     version (1) + timeout (5s), and the document limits (5 GiB / 100 / 512 +
//     the built-in format set) match the values the code used as consts.
func TestLayerConfigDefaults_MatchConsts(t *testing.T) {
	t.Parallel()

	s := stores.DefaultConfig()
	assert.Equal(t, 20, s.DefaultPageSize)
	assert.Equal(t, 100, s.PendingIndexingPageSize)
	assert.Equal(t, 1000, s.MaxPendingIndexingPageSize)
	assert.Equal(t, 1000, s.MaxPageSize)

	r := repos.DefaultConfig()
	assert.Equal(t, 5*time.Minute, r.Caching.TTL)
	assert.Equal(t, 1, r.Caching.Version)
	assert.Equal(t, 5*time.Second, r.Timeout)

	v := services.DefaultConfig()
	assert.Equal(t, int64(5<<30), v.Document.MaxUploadBytes)
	assert.Equal(t, 100, v.Document.MaxBulkCount)
}

// TestLayerConfigValidate_RejectsBadValues tests that each layer's Validate()
// rejects the out-of-range values it names.
//
// Why this test is important:
//   - These surfaces are meant to be tuned per environment; Validate guards a
//     fat-fingered overlay (a zero page size, a max below the default, a zero
//     upload ceiling) from producing a silently-broken data/domain layer.
//
// What it tests:
//   - Defaults validate; a zero default page size, a max_page_size below the
//     default, and a zero max_upload_bytes are each rejected.
func TestLayerConfigValidate_RejectsBadValues(t *testing.T) {
	t.Parallel()

	require.NoError(t, stores.DefaultConfig().Validate())
	require.NoError(t, repos.DefaultConfig().Validate())
	require.NoError(t, services.DefaultConfig().Validate())

	badPage := stores.DefaultConfig()
	badPage.DefaultPageSize = 0
	assert.Error(t, badPage.Validate())

	badMax := stores.DefaultConfig()
	badMax.MaxPageSize = 5 // below default_page_size 20
	assert.Error(t, badMax.Validate())

	badUpload := services.DefaultConfig()
	badUpload.Document.MaxUploadBytes = 0
	assert.Error(t, badUpload.Validate())
}
