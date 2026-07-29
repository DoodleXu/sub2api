package service

import (
	"os"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestMain initializes process-wide Gin state before parallel tests begin.
// Individual tests must not mutate Gin's global mode after calling t.Parallel.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}
