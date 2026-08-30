package siteclient

// This file contains the first-party desktop device protocol. The private key
// and PKCE verifier are kept in memory during authorization; after a successful
// exchange the App binding persists the DPoP private key and refresh token in
// the OS credential store so a restart can renew the session. Access tokens
// remain process-memory only.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// DesktopClientID and DesktopAudience are duplicated here (rather than
	// importing backend code) so the released desktop module remains
	// independently buildable.
	DesktopClientID  = "sub2api-desktop"
	DesktopAudience  = "sub2api-api"
	DesktopGrantType = "urn:ietf:params:oauth:grant-type:device_code"

	// OfficialSiteURL is the first-party bootstrap origin.  The desktop client
	// deliberately pins this value so a phishing or unknown origin cannot be
	// smuggled into either the device-login or API-key flow.
	OfficialSiteURL = "https://ai.clol.site"
)

var ErrDeviceProofExpired = errors.New("desktop device proof is no longer available; start again")

// DeviceProof is a proof for one authorization request or restored device
// session. Its private key is never exposed through the Wails DTOs; the App
// binding serializes it only into the OS credential store when persistence is
// required for refresh-token rotation.
type DeviceProof struct {
	privateKey *ecdsa.PrivateKey
	publicJWK  json.RawMessage
	verifier   string
	challenge  string
	nonceMu    sync.RWMutex
	nonce      string
}

// NewDeviceProof creates an EC P-256 JWK and an RFC 7636 S256 verifier.  The
// server currently requires exactly this curve and proof method.
func NewDeviceProof() (*DeviceProof, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate device key: %w", err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	publicJWK, err := json.Marshal(map[string]string{
		"crv": "P-256",
		"kty": "EC",
		"x":   encode(pad32(privateKey.PublicKey.X.Bytes())),
		"y":   encode(pad32(privateKey.PublicKey.Y.Bytes())),
	})
	if err != nil {
		return nil, fmt.Errorf("encode device key: %w", err)
	}
	// 32 random bytes become a 43-character URL-safe verifier, within RFC
	// 7636's 43..128 byte range.
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("generate PKCE verifier: %w", err)
	}
	verifier := encode(seed)
	challengeSum := sha256.Sum256([]byte(verifier))
	return &DeviceProof{
		privateKey: privateKey,
		publicJWK:  publicJWK,
		verifier:   verifier,
		challenge:  encode(challengeSum[:]),
	}, nil
}

func IsOfficialSiteURL(value string) bool {
	parsed, err := normalizeBaseURL(value)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "https") && strings.EqualFold(parsed.Host, "ai.clol.site")
}

func pad32(value []byte) []byte {
	if len(value) >= 32 {
		return value[len(value)-32:]
	}
	padded := make([]byte, 32)
	copy(padded[32-len(value):], value)
	return padded
}

func (p *DeviceProof) publicKey() json.RawMessage {
	if p == nil {
		return nil
	}
	return append(json.RawMessage(nil), p.publicJWK...)
}

func (p *DeviceProof) verifierValue() string {
	if p == nil {
		return ""
	}
	return p.verifier
}

func (p *DeviceProof) challengeValue() string {
	if p == nil {
		return ""
	}
	return p.challenge
}

// MarshalPrivate serializes only the private key needed to recreate DPoP
// proofs. It is intended for the OS keyring, never for connection.json or an
// HTTP request. The PKCE verifier/challenge are not persisted after exchange.
func (p *DeviceProof) MarshalPrivate() ([]byte, error) {
	if p == nil || p.privateKey == nil {
		return nil, ErrDeviceProofExpired
	}
	encode := base64.RawURLEncoding.EncodeToString
	return json.Marshal(map[string]string{
		"kty": "EC",
		"crv": "P-256",
		"x":   encode(pad32(p.privateKey.PublicKey.X.Bytes())),
		"y":   encode(pad32(p.privateKey.PublicKey.Y.Bytes())),
		"d":   encode(pad32(p.privateKey.D.Bytes())),
	})
}

// RestorePrivate reconstructs a proof from keyring material and rejects
// malformed or off-curve key material before it can sign a request.
func RestorePrivate(raw []byte) (*DeviceProof, error) {
	var key struct {
		KTY string `json:"kty"`
		CRV string `json:"crv"`
		X   string `json:"x"`
		Y   string `json:"y"`
		D   string `json:"d"`
	}
	if err := json.Unmarshal(raw, &key); err != nil || key.KTY != "EC" || key.CRV != "P-256" || key.X == "" || key.Y == "" || key.D == "" {
		return nil, errors.New("invalid desktop proof key")
	}
	decode := base64.RawURLEncoding.Strict().DecodeString
	xBytes, errX := decode(key.X)
	yBytes, errY := decode(key.Y)
	dBytes, errD := decode(key.D)
	if errX != nil || errY != nil || errD != nil || len(xBytes) != 32 || len(yBytes) != 32 || len(dBytes) != 32 {
		return nil, errors.New("invalid desktop proof key")
	}
	x, y, d := new(big.Int).SetBytes(xBytes), new(big.Int).SetBytes(yBytes), new(big.Int).SetBytes(dBytes)
	curve := elliptic.P256()
	if !curve.IsOnCurve(x, y) || d.Sign() <= 0 || d.Cmp(curve.Params().N) >= 0 {
		return nil, errors.New("invalid desktop proof key")
	}
	private := &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y}, D: d}
	publicJWK, err := json.Marshal(map[string]string{"crv": "P-256", "kty": "EC", "x": key.X, "y": key.Y})
	if err != nil {
		return nil, errors.New("invalid desktop proof key")
	}
	return &DeviceProof{privateKey: private, publicJWK: publicJWK}, nil
}

func (p *DeviceProof) setNonce(value string) {
	if p != nil {
		p.nonceMu.Lock()
		p.nonce = strings.TrimSpace(value)
		p.nonceMu.Unlock()
	}
}

// SetNonce updates the server-issued nonce used by subsequent DPoP proofs.
// It is exported for the desktop binding's secure-token restore path.
func (p *DeviceProof) SetNonce(value string) {
	p.setNonce(value)
}

// DPoPNonce returns the latest server-issued nonce attached to this proof.
// It exposes no key material and is useful to the desktop binding when it
// verifies that a restored proof is bound to the persisted session state.
func (p *DeviceProof) DPoPNonce() string {
	return p.nonceValue()
}

func (p *DeviceProof) nonceValue() string {
	if p == nil {
		return ""
	}
	p.nonceMu.RLock()
	defer p.nonceMu.RUnlock()
	return p.nonce
}

// signDPoP creates the compact proof accepted by the server middleware. JOSE
// ES256 signatures are encoded as fixed-width R||S rather than ASN.1 DER.
func (p *DeviceProof) signDPoP(method, targetURL, accessToken string) (string, error) {
	if p == nil || p.privateKey == nil {
		return "", errors.New("desktop DPoP nonce is unavailable")
	}
	nonce := p.nonceValue()
	if nonce == "" {
		return "", errors.New("desktop DPoP nonce is unavailable")
	}
	targetURL, err := canonicalDPoPTarget(targetURL)
	if err != nil {
		return "", err
	}
	encode := base64.RawURLEncoding.EncodeToString
	header, err := json.Marshal(map[string]any{"typ": "dpop+jwt", "alg": "ES256", "jwk": json.RawMessage(p.publicKey())})
	if err != nil {
		return "", err
	}
	jti, err := randomBytes(16)
	if err != nil {
		return "", fmt.Errorf("generate DPoP jti: %w", err)
	}
	claims := map[string]any{
		"htu":   targetURL,
		"htm":   strings.ToUpper(method),
		"iat":   time.Now().Unix(),
		"jti":   encode(jti),
		"nonce": nonce,
	}
	if strings.TrimSpace(accessToken) != "" {
		athSum := sha256.Sum256([]byte(accessToken))
		claims["ath"] = encode(athSum[:])
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	input := encode(header) + "." + encode(payload)
	hash := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, p.privateKey, hash[:])
	if err != nil {
		return "", err
	}
	signature := make([]byte, 64)
	rBytes, sBytes := r.Bytes(), s.Bytes()
	copy(signature[32-len(rBytes):32], rBytes)
	copy(signature[64-len(sBytes):], sBytes)
	return input + "." + encode(signature), nil
}

func randomBytes(length int) ([]byte, error) {
	value := make([]byte, length)
	if _, err := rand.Read(value); err != nil {
		return nil, err
	}
	return value, nil
}

// canonicalDPoPTarget follows the server's DPoP htu canonicalization: query
// and fragment components are excluded, while the scheme/host/path remain
// bound to the request. This matters for signed list/pagination requests.
func canonicalDPoPTarget(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil {
		return "", errors.New("invalid DPoP target URL")
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.RawPath = ""
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

type DeviceAuthorizationRequest struct {
	ClientID        string
	DeviceName      string
	Scopes          []string
	Audience        string
	ProtectionLevel string
	Proof           *DeviceProof
}

type DeviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	ClientID                string `json:"client_id"`
	Scope                   string `json:"scope"`
	Audience                string `json:"audience"`
}

type DeviceTokenRequest struct {
	DeviceCode string
	ClientID   string
	Audience   string
	Proof      *DeviceProof
}

type DeviceToken struct {
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	ExpiresIn    int         `json:"expires_in"`
	TokenType    string      `json:"token_type"`
	Scope        string      `json:"scope"`
	Audience     string      `json:"audience"`
	DPoPNonce    string      `json:"dpop_nonce,omitempty"`
	Device       *DeviceInfo `json:"device,omitempty"`
}

// SetDeviceProof enables DPoP on subsequent requests carrying a bearer token.
// Passing nil disables it. The proof object is retained by the caller so its
// key can remain in memory while the HTTP client is in use.
func (c *HTTPClient) SetDeviceProof(proof *DeviceProof, nonce string) {
	if c == nil {
		return
	}
	c.proofMu.Lock()
	if proof != nil {
		proof.setNonce(nonce)
	}
	c.deviceProof = proof
	c.proofMu.Unlock()
}

func (c *HTTPClient) updateDPoPNonce(nonce string) {
	nonce = strings.TrimSpace(nonce)
	if c == nil || nonce == "" {
		return
	}
	c.proofMu.Lock()
	if c.deviceProof != nil {
		c.deviceProof.setNonce(nonce)
	}
	c.proofMu.Unlock()
}

func (c *HTTPClient) dpopProof(method, endpoint, accessToken string) (string, error) {
	c.proofMu.RLock()
	defer c.proofMu.RUnlock()
	proof := c.deviceProof
	if proof == nil {
		return "", nil
	}
	return proof.signDPoP(method, endpoint, accessToken)
}

// DPoPNonce returns the latest server-issued nonce held by this client. It is
// exposed so the desktop binding can persist nonce rotation alongside token
// rotation; the private key itself remains inside DeviceProof/keyring.
func (c *HTTPClient) DPoPNonce() string {
	if c == nil {
		return ""
	}
	c.proofMu.RLock()
	defer c.proofMu.RUnlock()
	if c.deviceProof == nil {
		return ""
	}
	return c.deviceProof.nonceValue()
}

type DeviceInfo struct {
	DeviceID        string   `json:"device_id"`
	ClientID        string   `json:"client_id"`
	DeviceName      string   `json:"device_name"`
	Scopes          []string `json:"scopes"`
	Audience        string   `json:"audience"`
	ProtectionLevel string   `json:"protection_level,omitempty"`
	CreatedAt       string   `json:"created_at,omitempty"`
	LastSeenAt      string   `json:"last_seen_at,omitempty"`
	RevokedAt       *string  `json:"revoked_at,omitempty"`
}

// AccountProfile is the intentionally small account view needed by the
// desktop dashboard. Unknown server fields are ignored so newer web builds
// remain compatible with an older desktop binary.
type AccountProfile struct {
	ID            int64   `json:"id"`
	Email         string  `json:"email"`
	Username      string  `json:"username"`
	Role          string  `json:"role"`
	Balance       float64 `json:"balance"`
	FrozenBalance float64 `json:"frozen_balance"`
	Status        string  `json:"status"`
}

type AccountBalance struct {
	Balance        float64   `json:"balance"`
	FrozenBalance  float64   `json:"frozen_balance"`
	TotalRecharged float64   `json:"total_recharged"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (c *HTTPClient) Profile(ctx context.Context, accessToken string) (AccountProfile, error) {
	if strings.TrimSpace(accessToken) == "" {
		return AccountProfile{}, errors.New("account access token is required")
	}
	var profile AccountProfile
	if err := c.doJSON(ctx, http.MethodGet, c.siteEndpoint("/user/profile"), accessToken, nil, &profile); err != nil {
		return AccountProfile{}, err
	}
	return profile, nil
}

func (c *HTTPClient) Balance(ctx context.Context, accessToken string) (AccountBalance, error) {
	if strings.TrimSpace(accessToken) == "" {
		return AccountBalance{}, errors.New("account access token is required")
	}
	var balance AccountBalance
	if err := c.doJSON(ctx, http.MethodGet, c.siteEndpoint("/user/balance"), accessToken, nil, &balance); err != nil {
		return AccountBalance{}, err
	}
	return balance, nil
}

type DeviceOAuthError struct {
	Code        string
	Description string
	StatusCode  int
}

func decodeDeviceOAuthError(status int, data []byte) *DeviceOAuthError {
	var payload struct {
		Code        string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || strings.TrimSpace(payload.Code) == "" {
		return nil
	}
	return &DeviceOAuthError{
		Code:        strings.TrimSpace(payload.Code),
		Description: strings.TrimSpace(payload.Description),
		StatusCode:  status,
	}
}

func (e *DeviceOAuthError) Error() string {
	if e == nil {
		return "desktop device authorization failed"
	}
	if e.Description != "" {
		return e.Description
	}
	if e.Code != "" {
		return e.Code
	}
	return "desktop device authorization failed"
}

func IsAuthorizationPending(err error) bool {
	var oauthErr *DeviceOAuthError
	return errors.As(err, &oauthErr) && oauthErr.Code == "authorization_pending"
}

func IsAuthorizationDenied(err error) bool {
	var oauthErr *DeviceOAuthError
	return errors.As(err, &oauthErr) && (oauthErr.Code == "access_denied" || oauthErr.Code == "expired_token")
}

func (c *HTTPClient) BeginDeviceAuthorization(ctx context.Context, request DeviceAuthorizationRequest) (DeviceAuthorization, error) {
	if request.Proof == nil || request.Proof.challengeValue() == "" {
		return DeviceAuthorization{}, errors.New("device proof is required")
	}
	clientID := strings.TrimSpace(request.ClientID)
	if clientID == "" {
		clientID = DesktopClientID
	}
	audience := strings.TrimSpace(request.Audience)
	if audience == "" {
		audience = DesktopAudience
	}
	deviceName := strings.TrimSpace(request.DeviceName)
	if deviceName == "" {
		deviceName = "神奇AI助手"
	}
	scopes := normalizeScopes(request.Scopes)
	body := map[string]any{
		"client_id":             clientID,
		"device_name":           deviceName,
		"public_key":            json.RawMessage(request.Proof.publicKey()),
		"code_challenge":        request.Proof.challengeValue(),
		"code_challenge_method": "S256",
		"scope":                 strings.Join(scopes, " "),
		"audience":              audience,
		"protection_level":      strings.TrimSpace(request.ProtectionLevel),
	}
	var result DeviceAuthorization
	if err := c.doJSON(ctx, http.MethodPost, c.siteEndpoint("/desktop/device-authorizations"), "", body, &result); err != nil {
		return DeviceAuthorization{}, err
	}
	if result.DeviceCode == "" || result.UserCode == "" {
		return DeviceAuthorization{}, errors.New("desktop authorization response is incomplete")
	}
	if result.ClientID == "" {
		result.ClientID = clientID
	}
	if result.Audience == "" {
		result.Audience = audience
	}
	if result.Interval < 1 || result.Interval > 60 {
		result.Interval = 5
	}
	return result, nil
}

func normalizeScopes(scopes []string) []string {
	if len(scopes) == 0 {
		return []string{"openid", "profile"}
	}
	allowed := map[string]struct{}{
		"openid": {}, "profile": {}, "balance": {}, "usage": {},
		"billing": {}, "checkin": {}, "images": {}, "api_keys": {},
	}
	seen := make(map[string]struct{}, len(scopes))
	for _, raw := range scopes {
		for _, item := range strings.Fields(strings.ToLower(raw)) {
			if _, ok := allowed[item]; ok {
				seen[item] = struct{}{}
			}
		}
	}
	if len(seen) == 0 {
		return []string{"openid", "profile"}
	}
	out := make([]string, 0, len(seen))
	for _, item := range []string{"openid", "profile", "balance", "usage", "billing", "checkin", "images", "api_keys"} {
		if _, ok := seen[item]; ok {
			out = append(out, item)
			delete(seen, item)
		}
	}
	for item := range seen {
		out = append(out, item)
	}
	return out
}

func (c *HTTPClient) ExchangeDeviceAuthorization(ctx context.Context, request DeviceTokenRequest) (DeviceToken, error) {
	if request.Proof == nil || request.Proof.verifierValue() == "" {
		return DeviceToken{}, ErrDeviceProofExpired
	}
	deviceCode := strings.TrimSpace(request.DeviceCode)
	if deviceCode == "" {
		return DeviceToken{}, errors.New("device code is required")
	}
	clientID := strings.TrimSpace(request.ClientID)
	if clientID == "" {
		clientID = DesktopClientID
	}
	audience := strings.TrimSpace(request.Audience)
	if audience == "" {
		audience = DesktopAudience
	}
	body := map[string]any{
		"grant_type":    DesktopGrantType,
		"device_code":   deviceCode,
		"code_verifier": request.Proof.verifierValue(),
		"public_key":    json.RawMessage(request.Proof.publicKey()),
		"client_id":     clientID,
		"audience":      audience,
	}
	var result DeviceToken
	if err := c.doJSON(ctx, http.MethodPost, c.siteEndpoint("/desktop/token"), "", body, &result); err != nil {
		return DeviceToken{}, err
	}
	if strings.TrimSpace(result.AccessToken) == "" || strings.TrimSpace(result.RefreshToken) == "" {
		return DeviceToken{}, errors.New("desktop token response is incomplete")
	}
	if result.TokenType == "" {
		result.TokenType = "DPoP"
	}
	if result.DPoPNonce != "" {
		c.SetDeviceProof(request.Proof, result.DPoPNonce)
	}
	return result, nil
}

func (c *HTTPClient) RefreshToken(ctx context.Context, refreshToken string) (DeviceToken, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return DeviceToken{}, errors.New("refresh token is required")
	}
	var result DeviceToken
	body := map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"public_key":    json.RawMessage(c.devicePublicKey()),
		"client_id":     DesktopClientID,
		"audience":      DesktopAudience,
	}
	if err := c.doJSONWithDPoP(ctx, http.MethodPost, c.siteEndpoint("/desktop/token"), "", body, &result); err != nil {
		return DeviceToken{}, err
	}
	if result.AccessToken == "" || result.RefreshToken == "" {
		return DeviceToken{}, errors.New("refresh response is incomplete")
	}
	if result.TokenType == "" {
		result.TokenType = "DPoP"
	}
	// Some deployments return the rotated nonce in the JSON token envelope but
	// omit the response header.  Apply both channels so the very next protected
	// request signs with the server-issued nonce instead of the stale one.
	if strings.TrimSpace(result.DPoPNonce) != "" {
		c.updateDPoPNonce(result.DPoPNonce)
	}
	return result, nil
}

func (c *HTTPClient) devicePublicKey() json.RawMessage {
	if c == nil {
		return nil
	}
	c.proofMu.RLock()
	proof := c.deviceProof
	c.proofMu.RUnlock()
	if proof == nil {
		return nil
	}
	return proof.publicKey()
}

func (c *HTTPClient) LogoutDevice(ctx context.Context, refreshToken string) error {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil
	}
	// The logout endpoint revokes a sender-constrained desktop session. Never
	// put a refresh token on the wire when the private DPoP key is unavailable:
	// the server will reject the request, and a future transport or compatibility
	// route must not accidentally downgrade this operation to bearer-only auth.
	if !c.hasDeviceProof() {
		return errors.New("desktop logout requires a DPoP device proof")
	}
	// Treat a refresh token in the JSON body as authenticated material even
	// though this endpoint deliberately has no Authorization header. The secure
	// request path rejects plaintext HTTP before the token reaches a transport.
	return c.doJSONWithDPoP(ctx, http.MethodPost, c.siteEndpoint("/desktop/logout"), "", map[string]string{"refresh_token": refreshToken}, nil)
}

func (c *HTTPClient) OpenVerificationURL(auth DeviceAuthorization) (string, error) {
	value := strings.TrimSpace(auth.VerificationURIComplete)
	if value == "" {
		value = strings.TrimSpace(auth.VerificationURI)
	}
	if value == "" {
		return "", errors.New("device verification URL is empty")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	if parsed.IsAbs() {
		if parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, c.siteURL.Host) {
			return "", ErrInvalidBaseURL
		}
		return parsed.String(), nil
	}
	base := *c.siteURL
	base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(parsed.Path, "/")
	base.RawQuery = parsed.RawQuery
	base.Fragment = parsed.Fragment
	return base.String(), nil
}
