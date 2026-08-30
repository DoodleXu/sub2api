package middleware

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// DefaultDesktopPublicOrigin is the only public origin accepted by the
// first-party desktop protocol. Keeping this value server-side prevents a
// caller from turning an untrusted Host/X-Forwarded-Host header into the DPoP
// target or a payment return URL.
const DefaultDesktopPublicOrigin = "https://ai.clol.site"

// DesktopTransportPolicy describes the ingress boundary for desktop
// requests. Forwarded headers are accepted only when the immediate peer is in
// TrustedProxies. PublicOrigin is normally the fixed first-party origin above;
// an empty value is useful for isolated tests and legacy integrations that do
// not use a reverse proxy.
type DesktopTransportPolicy struct {
	TrustedProxies []netip.Prefix
	PublicOrigin   string
}

// DesktopTransportPolicyForConfig builds the production ingress policy from
// server configuration. An explicitly configured frontend URL is accepted as
// the deployment's first-party origin; otherwise the pinned official origin is
// used.
func DesktopTransportPolicyForConfig(trusted []string, configuredOrigin string) DesktopTransportPolicy {
	origin := DefaultDesktopPublicOrigin
	// `server.frontend_url` is allowed to contain an application path (for
	// example https://example.test/console).  It is not an origin in the URL
	// specification, so normalize it to scheme+authority before using it for
	// DPoP Host pinning.  If an administrator supplies an invalid value, retain
	// the first-party default instead of silently creating an unpinned policy.
	if normalized := normalizePublicOrigin(configuredOrigin); normalized != "" {
		origin = normalized
	}
	return NewDesktopTransportPolicy(trusted, origin)
}

// NewDesktopTransportPolicy parses the same IP/CIDR notation used by
// server.trusted_proxies. Invalid entries are ignored here; configuration
// validation remains responsible for rejecting malformed production values.
func NewDesktopTransportPolicy(trusted []string, publicOrigin string) DesktopTransportPolicy {
	normalizedOrigin := normalizePublicOrigin(publicOrigin)
	if strings.TrimSpace(publicOrigin) != "" && normalizedOrigin == "" {
		// A non-empty but malformed origin must never disable authority pinning.
		normalizedOrigin = DefaultDesktopPublicOrigin
	}
	policy := DesktopTransportPolicy{PublicOrigin: normalizedOrigin}
	for _, raw := range trusted {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(value); err == nil {
			policy.TrustedProxies = append(policy.TrustedProxies, prefix)
			continue
		}
		if addr, err := netip.ParseAddr(value); err == nil {
			bits := 128
			if addr.Is4() {
				bits = 32
			}
			policy.TrustedProxies = append(policy.TrustedProxies, netip.PrefixFrom(addr, bits))
		}
	}
	return policy
}

// verifyDesktopDPoP enforces proof-of-possession for desktop JWTs. Browser
// tokens do not carry DeviceID and therefore never enter this path.
func verifyDesktopDPoP(c *gin.Context, claims *service.JWTClaims, accessToken string, checker service.DeviceSessionRevocationChecker, policies ...DesktopTransportPolicy) bool {
	verifier, ok := checker.(service.DeviceProofVerifier)
	if !ok || verifier == nil {
		AbortWithError(c, http.StatusServiceUnavailable, "DEVICE_SESSION_UNAVAILABLE", "Device session validation is temporarily unavailable")
		return false
	}
	proof := strings.TrimSpace(c.GetHeader("DPoP"))
	nonce, err := verifier.VerifyDeviceProof(c.Request.Context(), claims.DeviceID, proof, c.Request.Method, requestURLForDPoPWithPolicy(c.Request, firstDesktopPolicy(policies)), accessToken)
	if nonce != "" {
		// A nonce is useful even for a failed proof so a client can retry after a
		// clock skew or a server-side nonce rotation. It never contains secrets.
		c.Header("DPoP-Nonce", nonce)
	}
	if err == nil {
		return true
	}
	switch {
	case errors.Is(err, service.ErrDesktopDeviceRevoked):
		AbortWithError(c, http.StatusUnauthorized, "DEVICE_SESSION_REVOKED", "Device session has been revoked")
	case errors.Is(err, service.ErrDesktopProofInvalid):
		AbortWithError(c, http.StatusUnauthorized, "DPOP_INVALID", "DPoP proof is invalid")
	default:
		AbortWithError(c, http.StatusServiceUnavailable, "DEVICE_SESSION_UNAVAILABLE", "Device session validation is temporarily unavailable")
	}
	return false
}

// RequireHTTPS protects the desktop device protocol before any credential is
// accepted or returned. In a TLS-terminating deployment the edge must provide
// exactly one normalized X-Forwarded-Proto: https value; cleartext and
// contradictory forwarding headers fail closed.
func RequireHTTPS(policies ...DesktopTransportPolicy) gin.HandlerFunc {
	policy := firstDesktopPolicy(policies)
	return func(c *gin.Context) {
		if c == nil || !isHTTPSRequestWithPolicy(c.Request, policy) {
			AbortWithError(c, http.StatusBadRequest, "HTTPS_REQUIRED", "This endpoint requires HTTPS")
			return
		}
		c.Set(string(ContextKeyDesktopTransportPolicy), policy)
		c.Next()
	}
}

func isHTTPSRequest(r *http.Request) bool {
	return isHTTPSRequestWithPolicy(r, DesktopTransportPolicy{})
}

func isHTTPSRequestWithPolicy(r *http.Request, policy DesktopTransportPolicy) bool {
	if r == nil {
		return false
	}
	secure := r.TLS != nil
	if forwarded, present := singleForwardedHeader(r.Header, "X-Forwarded-Proto"); present {
		if !isTrustedProxyPeer(r, policy) || !isNormalizedForwardedValue(forwarded) || forwarded != "https" {
			return false
		}
		secure = true
	}
	if !secure {
		return false
	}
	return effectiveDesktopHost(r, policy) != ""
}

// RequestURLForDPoP builds the URL form used by the DPoP htu claim. Query and
// fragment components are intentionally omitted by the service canonicalizer.
// Forwarded host/proto are honored because the API normally sits behind a TLS
// reverse proxy, but only as one already-normalized header value. A client-
// supplied comma list or control/authority delimiter is rejected rather than
// selecting the first value and allowing header smuggling to alter the proof
// target. Desktop proof targets are HTTPS-only: cleartext requests must not
// become valid merely because a client can mint a DPoP proof for an http URL.
func RequestURLForDPoP(r *http.Request) string {
	return requestURLForDPoPWithPolicy(r, DesktopTransportPolicy{})
}

// RequestURLForDPoPWithPolicy is the policy-aware form used by production
// middleware. It refuses forwarded headers from an untrusted peer and, when a
// public origin is configured, requires the effective request authority to
// match that origin exactly.
func RequestURLForDPoPWithPolicy(r *http.Request, policy DesktopTransportPolicy) string {
	return requestURLForDPoPWithPolicy(r, policy)
}

func requestURLForDPoPWithPolicy(r *http.Request, policy DesktopTransportPolicy) string {
	if r == nil {
		return ""
	}
	if !isHTTPSRequestWithPolicy(r, policy) {
		return ""
	}
	scheme := "https"
	if forwarded, present := singleForwardedHeader(r.Header, "X-Forwarded-Proto"); present {
		scheme = forwarded
	}
	host := effectiveDesktopHost(r, policy)
	if host == "" {
		return ""
	}
	if r.URL == nil {
		return ""
	}
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	return scheme + "://" + host + path
}

// requestURLForDPoP is kept as a package-local alias for existing tests and
// integrations; new callers should use RequestURLForDPoP.
func requestURLForDPoP(r *http.Request) string { return RequestURLForDPoP(r) }

// RequestURLForDPoPFromContext obtains the policy installed by RequireHTTPS.
// Handlers at the device token endpoint use this so token exchange and normal
// authenticated requests canonicalize the exact same target.
func RequestURLForDPoPFromContext(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	if value, ok := c.Get(string(ContextKeyDesktopTransportPolicy)); ok {
		if policy, ok := value.(DesktopTransportPolicy); ok {
			return requestURLForDPoPWithPolicy(c.Request, policy)
		}
	}
	return RequestURLForDPoP(c.Request)
}

// DesktopPublicOriginFromContext returns the configured canonical origin for
// browser-only hosted flows. It never derives an origin from an untrusted
// forwarded header.
func DesktopPublicOriginFromContext(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if value, ok := c.Get(string(ContextKeyDesktopTransportPolicy)); ok {
		if policy, ok := value.(DesktopTransportPolicy); ok && policy.PublicOrigin != "" {
			return policy.PublicOrigin
		}
	}
	return ""
}

func firstDesktopPolicy(policies []DesktopTransportPolicy) DesktopTransportPolicy {
	if len(policies) == 0 {
		return DesktopTransportPolicy{}
	}
	return policies[0]
}

func isTrustedProxyPeer(r *http.Request, policy DesktopTransportPolicy) bool {
	if r == nil || len(policy.TrustedProxies) == 0 {
		return false
	}
	peer := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	addr, err := netip.ParseAddr(strings.Trim(peer, "[]"))
	if err != nil {
		return false
	}
	for _, prefix := range policy.TrustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func effectiveDesktopHost(r *http.Request, policy DesktopTransportPolicy) string {
	if r == nil {
		return ""
	}
	host := strings.TrimSpace(r.Host)
	if forwarded, present := singleForwardedHeader(r.Header, "X-Forwarded-Host"); present {
		if !isTrustedProxyPeer(r, policy) || !isNormalizedForwardedHost(forwarded) {
			return ""
		}
		host = forwarded
	}
	if !isNormalizedForwardedHost(host) {
		return ""
	}
	if policy.PublicOrigin != "" {
		parsed, err := url.Parse(policy.PublicOrigin)
		if err != nil || !strings.EqualFold(parsed.Host, host) {
			return ""
		}
	}
	return host
}

func normalizePublicOrigin(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || !strings.EqualFold(u.Scheme, "https") || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	// Ignore an application path deliberately: the value is used as an HTTP
	// origin/authority for DPoP and hosted checkout, not as a route prefix.
	// Validate the authority with the same strict ASCII parser used for
	// forwarded hosts so values such as userinfo, delimiters, or control bytes
	// cannot become a trusted Host.
	host := strings.ToLower(strings.TrimSpace(u.Host))
	if !isNormalizedForwardedHost(host) {
		return ""
	}
	return "https://" + host
}

func singleForwardedHeader(headers http.Header, name string) (string, bool) {
	var values []string
	for key, candidates := range headers {
		if strings.EqualFold(key, name) {
			values = append(values, candidates...)
		}
	}
	if len(values) == 0 {
		return "", false
	}
	if len(values) != 1 {
		return "", true
	}
	return values[0], true
}

func isNormalizedForwardedValue(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || strings.Contains(value, ",") {
		return false
	}
	for i := 0; i < len(value); i++ {
		// Keep forwarded authorities ASCII and reject all controls/whitespace.
		if value[i] <= 0x20 || value[i] == 0x7f || value[i] >= 0x80 {
			return false
		}
	}
	return true
}

func isNormalizedForwardedHost(value string) bool {
	if !isNormalizedForwardedValue(value) || strings.ContainsAny(value, "/\\?#@%") {
		return false
	}
	parsed, err := url.Parse("https://" + value + "/")
	return err == nil && parsed.User == nil && parsed.Host == value && parsed.Path == "/" && parsed.RawQuery == "" && parsed.Fragment == ""
}
