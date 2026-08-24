package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBatchImageIdempotencyUniqueMigration(t *testing.T) {
	content, err := FS.ReadFile("231_batch_image_idempotency_unique_notx.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS batch_image_jobs_owner_idempotency_unique_idx")
	require.Contains(t, sql, "ON batch_image_jobs (user_id, COALESCE(api_key_id, 0), idempotency_key)")
	require.Contains(t, sql, "WHERE idempotency_key IS NOT NULL AND idempotency_key <> ''")
}
