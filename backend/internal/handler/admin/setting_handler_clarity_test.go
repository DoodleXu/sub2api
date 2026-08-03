package admin

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestClaritySettingsParticipateInPartialUpdatesAndAudit(t *testing.T) {
	sentFields := map[string]json.RawMessage{
		"clarity_enabled":    json.RawMessage(`true`),
		"clarity_project_id": json.RawMessage(`"xwiilcm4jb"`),
	}
	omitted := omittedSettingKeys(sentFields)
	require.NotContains(t, omitted, service.SettingKeyClarityEnabled)
	require.NotContains(t, omitted, service.SettingKeyClarityProjectID)
	require.Contains(t, omitted, service.SettingKeySiteName)

	changed := diffSettings(
		&service.SystemSettings{},
		&service.SystemSettings{
			ClarityEnabled:   true,
			ClarityProjectID: "xwiilcm4jb",
		},
		nil,
		nil,
		UpdateSettingsRequest{},
	)
	require.Contains(t, changed, "clarity_enabled")
	require.Contains(t, changed, "clarity_project_id")
}
