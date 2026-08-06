package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// DynamicResponseNoStore marks all gateway and panel endpoints as private
// dynamic responses. This is a source-level defense in depth for CDNs and
// reverse proxies that are configured to cache despite origin defaults.
func DynamicResponseNoStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		if ShouldNoStoreDynamicPath(c.Request.URL.Path) {
			setNoStoreHeaders(c)
		}

		c.Next()

		// Handlers should not override this policy, but restoring it here also
		// covers handlers that set a generic cache header before returning.
		if ShouldNoStoreDynamicPath(c.Request.URL.Path) {
			setNoStoreHeaders(c)
		}
	}
}

func ShouldNoStoreDynamicPath(path string) bool {
	if path == "" {
		return false
	}

	for _, prefix := range []string{
		"/api/",
		"/v1/",
		"/v1beta/",
		"/responses/",
		"/backend-api/",
		"/alpha/",
		"/models/",
		"/messages/",
		"/chat/",
		"/embeddings/",
		"/images/",
		"/videos/",
		"/antigravity/",
	} {
		if path == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(path, prefix) {
			return true
		}
	}

	return false
}

func setNoStoreHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
	c.Header("Surrogate-Control", "no-store")
	appendVaryHeaders(c, "Authorization", "Cookie")
}

func appendVaryHeaders(c *gin.Context, values ...string) {
	seen := make(map[string]struct{})
	merged := make([]string, 0, len(values))
	for _, header := range c.Writer.Header().Values("Vary") {
		for _, value := range strings.Split(header, ",") {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			key := strings.ToLower(value)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, value)
		}
	}
	for _, value := range values {
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, value)
	}
	c.Header("Vary", strings.Join(merged, ", "))
}
