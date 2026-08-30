package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDesktopDevicesMigrationDefinesBoundSessionSchema(t *testing.T) {
	content, err := FS.ReadFile("234_desktop_devices.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS desktop_devices")
	require.Contains(t, sql, "user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE")
	require.Contains(t, sql, "public_key_thumbprint VARCHAR(128) NOT NULL")
	require.Contains(t, sql, "public_key_jwk JSONB NOT NULL")
	require.Contains(t, sql, "dpop_nonce VARCHAR(128) NOT NULL")
	require.Contains(t, sql, "protection_level VARCHAR(32) NOT NULL")
	require.Contains(t, sql, "scopes JSONB NOT NULL")
	require.Contains(t, sql, "session_id VARCHAR(128) NOT NULL UNIQUE")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_desktop_devices_user_created")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_desktop_devices_active")
	require.Contains(t, sql, "WHERE revoked_at IS NULL")
}
