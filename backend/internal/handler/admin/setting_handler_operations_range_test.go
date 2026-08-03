package admin

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestParseOperationsDateRangeUsesRequestedTimezoneAcrossDST(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("GET", "/?start_date=2026-10-31&end_date=2026-11-01&timezone=America%2FLos_Angeles", nil)

	start, end, err := parseOperationsDateRange(ctx, 30)
	require.NoError(t, err)
	require.Equal(t, "America/Los_Angeles", start.Location().String())
	require.Equal(t, "2026-10-31T00:00:00-07:00", start.Format(time.RFC3339))
	require.Equal(t, "2026-11-02T00:00:00-08:00", end.Format(time.RFC3339))
}

func TestParseOperationsDateRangeRejectsInvalidTimezone(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("GET", "/?timezone=Not%2FAZone", nil)

	_, _, err := parseOperationsDateRange(ctx, 30)
	require.EqualError(t, err, "invalid timezone")
}
