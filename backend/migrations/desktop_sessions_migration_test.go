package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDesktopSessionsMigrationDefinesDurableSessionLedger(t *testing.T) {
	content, err := FS.ReadFile("236_desktop_sessions.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS desktop_sessions")
	require.Contains(t, sql, "session_id VARCHAR(128) PRIMARY KEY")
	require.Contains(t, sql, "device_id VARCHAR(64) NOT NULL REFERENCES desktop_devices(device_id) ON DELETE CASCADE")
	require.Contains(t, sql, "refresh_family_id VARCHAR(128) NOT NULL")
	require.Contains(t, sql, "idle_expires_at TIMESTAMPTZ")
	require.Contains(t, sql, "absolute_expires_at TIMESTAMPTZ")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_desktop_sessions_user_created")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_desktop_sessions_device_active")
	require.Contains(t, sql, "CREATE OR REPLACE FUNCTION public.sync_desktop_session_from_device()")
	require.Contains(t, sql, "CREATE TRIGGER desktop_devices_sync_session")
	require.Contains(t, sql, "OLD.session_id IS DISTINCT FROM NEW.session_id")
	require.Contains(t, sql, "INSERT INTO desktop_sessions")
}
