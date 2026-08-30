package service

// Desktop device authorization is deliberately implemented separately from the
// browser login flow.  A desktop client never receives a user password and the
// server does not attempt to derive a hardware fingerprint (which would be both
// unreliable and privacy sensitive).  The device public key supplied by the
// client is reduced to an RFC 7638-style thumbprint and must be presented again
// when the short-lived authorization code is exchanged.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/redis/go-redis/v9"
)

const (
	// DesktopClientID is the first-party client id advertised by capabilities.
	// Keeping the value stable lets a desktop build pin the protocol surface.
	DesktopClientID = "sub2api-desktop"
	DesktopAudience = "sub2api-api"
	// DesktopPublicOrigin is the canonical first-party browser origin used by
	// the device authorization hand-off.  Returning an absolute URI here keeps
	// the OAuth contract independent of whichever API host or reverse proxy
	// happened to receive the request.
	DesktopPublicOrigin = "https://ai.clol.site"
	// DesktopGrantType is the RFC 8628 device authorization grant value. Keep
	// the literal in one place so the handler and independent desktop module
	// cannot silently drift into an ambiguous token request.
	DesktopGrantType = "urn:ietf:params:oauth:grant-type:device_code"

	// DesktopAuthorizationExpiresInSeconds is part of the public device-flow
	// contract and is intentionally shared by the capabilities document and the
	// Redis authorization record. Keep this as a single source of truth.
	DesktopAuthorizationExpiresInSeconds = 5 * 60

	// Keep the user-facing authorization code deliberately short-lived. The
	// desktop client receives a one-time code that is entered in a browser, so
	// five minutes is enough for normal hand-off while limiting the replay
	// window if the display is observed.
	desktopAuthorizationTTL   = time.Duration(DesktopAuthorizationExpiresInSeconds) * time.Second
	desktopAccessTokenTTL     = 10 * time.Minute
	desktopRefreshIdleTTL     = 30 * 24 * time.Hour
	desktopRefreshAbsoluteTTL = 90 * 24 * time.Hour
	desktopPollInterval       = 5
	desktopRevocationTTL      = 45 * 24 * time.Hour
	// Cache cleanup is not part of the enrollment commit.  A database row with
	// the new session family is the durable authority after a re-enrollment;
	// this bounded retry merely removes the old hot-path marker when Redis is
	// reachable again.  It must never turn a successful SQL upsert into a
	// failed enrollment.
	desktopRevocationCleanupTimeout = 2 * time.Second
	dpopClockSkew                   = 5 * time.Minute
	dpopReplayTTL                   = 10 * time.Minute

	desktopDeviceCodeBytes = 32
	desktopUserCodeBytes   = 5
	desktopFamilyIDBytes   = 16

	maxDesktopDeviceName       = 128
	maxDesktopCodeChallengeLen = 128
	maxDesktopCodeVerifierLen  = 128
	maxDesktopPublicKeyBytes   = 4096
)

const (
	desktopAuthKeyPrefix     = "desktop:device:authorization:"
	desktopUserCodeKeyPrefix = "desktop:device:user-code:"
	desktopRevokedKeyPrefix  = "desktop:device:revoked:"
	desktopStatusPending     = "pending"
	desktopStatusApproved    = "approved"
	desktopStatusDenied      = "denied"
	desktopStatusConsumed    = "consumed"
)

var (
	ErrDesktopInvalidRequest       = infraerrors.BadRequest("DESKTOP_INVALID_REQUEST", "invalid desktop authorization request")
	ErrDesktopClientInvalid        = infraerrors.BadRequest("DESKTOP_CLIENT_INVALID", "unsupported desktop client")
	ErrDesktopAudienceInvalid      = infraerrors.BadRequest("DESKTOP_AUDIENCE_INVALID", "unsupported token audience")
	ErrDesktopPKCERequired         = infraerrors.BadRequest("DESKTOP_PKCE_REQUIRED", "S256 PKCE is required")
	ErrDesktopPublicKeyRequired    = infraerrors.BadRequest("DESKTOP_PUBLIC_KEY_REQUIRED", "a device public key is required")
	ErrDesktopPublicKeyInvalid     = infraerrors.BadRequest("DESKTOP_PUBLIC_KEY_INVALID", "invalid or unsupported device public key")
	ErrDesktopAuthorizationPending = errors.New("desktop authorization pending")
	ErrDesktopAuthorizationDenied  = errors.New("desktop authorization denied")
	ErrDesktopAuthorizationExpired = errors.New("desktop authorization expired")
	ErrDesktopAuthorizationUsed    = errors.New("desktop authorization code already used")
	ErrDesktopProofInvalid         = errors.New("desktop proof invalid")
	ErrDesktopLogoutProofInvalid   = infraerrors.Unauthorized("DPOP_INVALID", "desktop logout proof is invalid")
	ErrDesktopDeviceNotFound       = infraerrors.NotFound("DESKTOP_DEVICE_NOT_FOUND", "desktop device not found")
	ErrDesktopDeviceRevoked        = infraerrors.Unauthorized("DESKTOP_DEVICE_REVOKED", "desktop device has been revoked")
	ErrDesktopDeviceSelfOnly       = infraerrors.Forbidden("DESKTOP_DEVICE_SELF_ONLY", "a desktop session may revoke only its own device")
	ErrDesktopScopeInvalid         = infraerrors.BadRequest("DESKTOP_SCOPE_INVALID", "the approved scopes must be a non-empty subset of the requested scopes")
	// A public key is the device identity. A revoked row may be re-enrolled by
	// the same account, but an active row or a row owned by another account must
	// not be silently replaced by a new authorization exchange.
	ErrDesktopDeviceAlreadyActive = infraerrors.Conflict("DESKTOP_DEVICE_ALREADY_ACTIVE", "this device is already enrolled")
	ErrDesktopDeviceKeyOwned      = infraerrors.Conflict("DESKTOP_DEVICE_KEY_OWNED", "this device public key is associated with another account")
)

// DeviceSessionRevocationChecker is consumed by JWT middleware to reject an
// access token immediately after a desktop device is revoked.  Ordinary browser
// tokens have no DeviceID and do not hit this check.
type DeviceSessionRevocationChecker interface {
	IsDeviceSessionRevoked(ctx context.Context, deviceID string) (bool, error)
}

// DeviceSessionTokenChecker additionally binds an access token to the
// currently enrolled refresh-token family.  A device public key may be
// re-enrolled after revocation; checking only device_id would otherwise allow
// an old access token to become valid again for the ten-minute JWT lifetime.
type DeviceSessionTokenChecker interface {
	DeviceSessionRevocationChecker
	IsDeviceSessionRevokedForSession(ctx context.Context, deviceID, sessionID string) (bool, error)
}

// DeviceProofVerifier is implemented by the desktop session service. The JWT
// middleware invokes it only for tokens that carry a DeviceID, leaving browser
// JWTs on their existing path while binding desktop requests to the P-256 key
// created during enrollment.
type DeviceProofVerifier interface {
	VerifyDeviceProof(ctx context.Context, deviceID, proof, method, targetURL, accessToken string) (nonce string, err error)
}

// DesktopDeviceSQL is the tiny SQL surface needed by this service. *sql.DB
// implements it, while tests can inject sqlmock without introducing a second
// repository package solely for this flow.
type DesktopDeviceSQL interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DesktopDeviceService coordinates Redis authorization records, persistent
// device sessions, and AuthService token rotation.
type DesktopDeviceService struct {
	db    DesktopDeviceSQL
	redis *redis.Client
	auth  *AuthService
	users UserRepository
}

func NewDesktopDeviceService(db *sql.DB, redisClient *redis.Client, auth *AuthService, users UserRepository) *DesktopDeviceService {
	return &DesktopDeviceService{db: db, redis: redisClient, auth: auth, users: users}
}

// NewDesktopDeviceServiceWithStore exists for focused unit tests and keeps the
// production constructor's dependency graph concrete for Wire.
func NewDesktopDeviceServiceWithStore(db DesktopDeviceSQL, redisClient *redis.Client, auth *AuthService, users UserRepository) *DesktopDeviceService {
	return &DesktopDeviceService{db: db, redis: redisClient, auth: auth, users: users}
}

type DesktopDeviceAuthorizationInput struct {
	ClientID            string
	DeviceName          string
	PublicKey           json.RawMessage
	CodeChallenge       string
	CodeChallengeMethod string
	Scopes              []string
	Audience            string
	ProtectionLevel     string
}

type DesktopDeviceAuthorization struct {
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

// DesktopDeviceAuthorizationApproval is the credential-free summary shown in
// the authenticated browser before a user approves a desktop device. It never
// exposes the device code, public key, PKCE challenge, or Redis identifiers.
type DesktopDeviceAuthorizationApproval struct {
	ClientID        string   `json:"client_id"`
	DeviceName      string   `json:"device_name"`
	Scopes          []string `json:"scopes"`
	Audience        string   `json:"audience"`
	ProtectionLevel string   `json:"protection_level"`
	ExpiresIn       int      `json:"expires_in"`
}

type DesktopDeviceTokenInput struct {
	DeviceCode   string
	CodeVerifier string
	PublicKey    json.RawMessage
	ClientID     string
	Audience     string
}

// DesktopDeviceRefreshInput is used by the desktop token endpoint when a
// refresh token is rotated. The DPoP proof is carried in the HTTP header and
// the public key is repeated in the body so a refresh cannot be detached from
// the enrolled device by a copied token alone.
type DesktopDeviceRefreshInput struct {
	ClientID     string
	Audience     string
	RefreshToken string
	PublicKey    json.RawMessage
	DPoPProof    string
	Method       string
	TargetURL    string
}

// DesktopDeviceProofInput carries the request-bound proof for operations that
// do not have an access token yet (refresh/logout). The proof is never persisted
// and is checked against the public key enrolled for the refresh-token family.
type DesktopDeviceProofInput struct {
	DPoPProof string
	Method    string
	TargetURL string
}

type DesktopDeviceToken struct {
	TokenPair
	TokenType string         `json:"token_type"`
	Scope     string         `json:"scope"`
	Audience  string         `json:"audience"`
	DPoPNonce string         `json:"dpop_nonce"`
	Device    *DesktopDevice `json:"device"`
}

type DesktopDevice struct {
	DeviceID            string     `json:"device_id"`
	ClientID            string     `json:"client_id"`
	DeviceName          string     `json:"device_name"`
	PublicKeyThumbprint string     `json:"public_key_thumbprint"`
	Scopes              []string   `json:"scopes"`
	Audience            string     `json:"audience"`
	ProtectionLevel     string     `json:"protection_level"`
	CreatedAt           time.Time  `json:"created_at"`
	LastSeenAt          time.Time  `json:"last_seen_at"`
	RevokedAt           *time.Time `json:"revoked_at,omitempty"`
}

type desktopAuthorizationRecord struct {
	Status              string
	ClientID            string
	DeviceName          string
	PublicKeyThumbprint string
	CodeChallenge       string
	CodeChallengeMethod string
	Scopes              []string
	Audience            string
	PublicKeyJWK        json.RawMessage
	ProtectionLevel     string
	UserID              int64
}

// CreateAuthorization stores a pending authorization request under hashes of
// both codes. Raw codes are returned only to the caller and are never persisted.
func (s *DesktopDeviceService) CreateAuthorization(ctx context.Context, input DesktopDeviceAuthorizationInput) (*DesktopDeviceAuthorization, error) {
	if s == nil || s.redis == nil {
		return nil, ErrServiceUnavailable
	}
	clientID := strings.TrimSpace(input.ClientID)
	if clientID != DesktopClientID {
		return nil, ErrDesktopClientInvalid
	}
	deviceName := strings.TrimSpace(input.DeviceName)
	if deviceName == "" {
		deviceName = "Desktop device"
	}
	if len([]rune(deviceName)) > maxDesktopDeviceName {
		return nil, ErrDesktopInvalidRequest
	}
	if err := validateDesktopPKCE(input.CodeChallenge, input.CodeChallengeMethod, false); err != nil {
		return nil, err
	}
	canonicalKey, thumbprint, err := CanonicalDevicePublicKey(input.PublicKey)
	if err != nil {
		return nil, err
	}
	scopes, err := NormalizeDesktopScopes(input.Scopes)
	if err != nil {
		return nil, err
	}
	audience := strings.TrimSpace(input.Audience)
	if audience == "" {
		audience = DesktopAudience
	}
	if audience != DesktopAudience {
		return nil, ErrDesktopAudienceInvalid
	}
	// The client cannot self-attest hardware-backed key storage. Until the
	// protocol carries a platform attestation, accept only the current OS
	// keyring/software levels and downgrade unknown or hardware claims.
	protectionLevel := normalizeRequestedProtectionLevel(input.ProtectionLevel)

	deviceCode, err := randomDesktopToken(desktopDeviceCodeBytes)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	userCodeRaw, err := randomDesktopToken(desktopUserCodeBytes)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	userCode := formatDesktopUserCode(userCodeRaw)
	deviceHash := hashDesktopCode(deviceCode)
	userHash := hashDesktopCode(userCode)
	key := desktopAuthKeyPrefix + deviceHash
	userKey := desktopUserCodeKeyPrefix + userHash
	expiresAt := time.Now().UTC().Add(desktopAuthorizationTTL)
	record := map[string]any{
		"status":                desktopStatusPending,
		"client_id":             clientID,
		"device_name":           deviceName,
		"public_key_thumbprint": thumbprint,
		"public_key_jwk":        string(canonicalKey),
		"protection_level":      protectionLevel,
		"code_challenge":        input.CodeChallenge,
		"code_challenge_method": "S256",
		"scopes":                strings.Join(scopes, " "),
		"audience":              audience,
		"expires_at":            expiresAt.Unix(),
	}
	pipe := s.redis.TxPipeline()
	for field, value := range record {
		pipe.HSet(ctx, key, field, value)
	}
	pipe.Expire(ctx, key, desktopAuthorizationTTL)
	pipe.Set(ctx, userKey, deviceHash, desktopAuthorizationTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, ErrServiceUnavailable
	}

	return &DesktopDeviceAuthorization{
		DeviceCode:              deviceCode,
		UserCode:                userCode,
		VerificationURI:         DesktopPublicOrigin + "/device",
		VerificationURIComplete: DesktopPublicOrigin + "/device?user_code=" + url.QueryEscape(userCode),
		ExpiresIn:               int(desktopAuthorizationTTL / time.Second),
		Interval:                desktopPollInterval,
		ClientID:                clientID,
		Scope:                   strings.Join(scopes, " "),
		Audience:                audience,
	}, nil
}

// GetAuthorizationForApproval returns the exact request that an authenticated
// browser is about to approve. Reading the summary never changes authorization
// state; the subsequent approval still validates the selected scope subset in
// one Redis script.
func (s *DesktopDeviceService) GetAuthorizationForApproval(ctx context.Context, userCode string) (*DesktopDeviceAuthorizationApproval, error) {
	if s == nil || s.redis == nil {
		return nil, ErrServiceUnavailable
	}
	userHash := hashDesktopCode(userCode)
	deviceHash, err := s.redis.Get(ctx, desktopUserCodeKeyPrefix+userHash).Result()
	if err == redis.Nil {
		return nil, ErrDesktopAuthorizationExpired
	}
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	values, err := s.redis.HGetAll(ctx, desktopAuthKeyPrefix+deviceHash).Result()
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	record, err := decodeDesktopAuthorizationRecord(values)
	if err != nil || record.Status != desktopStatusPending {
		return nil, ErrDesktopAuthorizationUsed
	}
	expiresAt := parseUnix(values["expires_at"])
	if expiresAt.IsZero() || !expiresAt.After(time.Now()) {
		return nil, ErrDesktopAuthorizationExpired
	}
	expiresIn := int(time.Until(expiresAt).Round(time.Second) / time.Second)
	if expiresIn < 0 {
		expiresIn = 0
	}
	return &DesktopDeviceAuthorizationApproval{
		ClientID:        record.ClientID,
		DeviceName:      record.DeviceName,
		Scopes:          append([]string(nil), record.Scopes...),
		Audience:        record.Audience,
		ProtectionLevel: record.ProtectionLevel,
		ExpiresIn:       expiresIn,
	}, nil
}

// ApproveAuthorization binds the pending code to the authenticated user. For
// an approval, selectedScopes must be a non-empty subset of what the device
// requested. The Redis script validates and writes that subset in the same
// state transition, preventing a stale browser preview or concurrent request
// from changing the final grant.
func (s *DesktopDeviceService) ApproveAuthorization(ctx context.Context, userID int64, userCode string, approved bool, selectedScopeArgs ...[]string) error {
	if s == nil || s.redis == nil || userID <= 0 {
		return ErrServiceUnavailable
	}
	var selectedScopes []string
	if len(selectedScopeArgs) > 0 {
		selectedScopes = selectedScopeArgs[0]
	}
	approvedScopeValue := ""
	if approved {
		if len(selectedScopes) == 0 {
			return ErrDesktopScopeInvalid
		}
		// Approval input is a concrete user selection, so a slice containing
		// only whitespace must not trigger NormalizeDesktopScopes' create-flow
		// default. Otherwise a tampered browser request could appear to approve
		// scopes that were never visibly selected.
		selectedScopeFields := make([]string, 0, len(selectedScopes))
		for _, raw := range selectedScopes {
			selectedScopeFields = append(selectedScopeFields, strings.Fields(raw)...)
		}
		if len(selectedScopeFields) == 0 {
			return ErrDesktopScopeInvalid
		}
		normalized, err := NormalizeDesktopScopes(selectedScopeFields)
		if err != nil || len(normalized) == 0 {
			return ErrDesktopScopeInvalid
		}
		approvedScopeValue = strings.Join(normalized, " ")
	}
	userHash := hashDesktopCode(userCode)
	deviceHash, err := s.redis.Get(ctx, desktopUserCodeKeyPrefix+userHash).Result()
	if err == redis.Nil {
		return ErrDesktopAuthorizationExpired
	}
	if err != nil {
		return ErrServiceUnavailable
	}
	status := desktopStatusDenied
	if approved {
		status = desktopStatusApproved
	}
	result, err := approveDesktopAuthorizationScript.Run(ctx, s.redis, []string{desktopAuthKeyPrefix + deviceHash}, status, userID, approvedScopeValue).Int()
	if err != nil {
		return ErrServiceUnavailable
	}
	switch result {
	case 1:
		return nil
	case -1:
		return ErrDesktopAuthorizationExpired
	case -2:
		return ErrDesktopAuthorizationUsed
	case -3:
		return ErrDesktopScopeInvalid
	default:
		return ErrDesktopAuthorizationExpired
	}
}

// ExchangeAuthorization verifies both PKCE and the device public-key binding,
// then atomically consumes the authorization code before issuing tokens.
func (s *DesktopDeviceService) ExchangeAuthorization(ctx context.Context, input DesktopDeviceTokenInput) (*DesktopDeviceToken, error) {
	if s == nil || s.redis == nil || s.auth == nil || s.users == nil {
		return nil, ErrServiceUnavailable
	}
	// Do not treat omitted client metadata as a legacy success path. The device
	// code is bearer-like until it is consumed, so the token endpoint must bind
	// every exchange to the fixed first-party client and audience before doing
	// any state transition.
	if strings.TrimSpace(input.ClientID) != DesktopClientID {
		return nil, ErrDesktopClientInvalid
	}
	if strings.TrimSpace(input.Audience) != DesktopAudience {
		return nil, ErrDesktopAudienceInvalid
	}
	deviceCode := strings.TrimSpace(input.DeviceCode)
	if deviceCode == "" {
		return nil, ErrDesktopAuthorizationExpired
	}
	deviceHash := hashDesktopCode(deviceCode)
	key := desktopAuthKeyPrefix + deviceHash
	values, err := s.redis.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	if len(values) == 0 {
		return nil, ErrDesktopAuthorizationExpired
	}
	record, err := decodeDesktopAuthorizationRecord(values)
	if err != nil {
		return nil, ErrDesktopAuthorizationExpired
	}
	if record.Status == desktopStatusPending {
		return nil, ErrDesktopAuthorizationPending
	}
	if record.Status == desktopStatusDenied {
		return nil, ErrDesktopAuthorizationDenied
	}
	if record.Status == desktopStatusConsumed {
		return nil, ErrDesktopAuthorizationUsed
	}
	if record.Status != desktopStatusApproved || (values["expires_at"] != "" && parseUnix(values["expires_at"]).Before(time.Now())) {
		return nil, ErrDesktopAuthorizationExpired
	}
	if strings.TrimSpace(input.ClientID) != record.ClientID {
		return nil, ErrDesktopProofInvalid
	}
	if strings.TrimSpace(input.Audience) != record.Audience {
		return nil, ErrDesktopProofInvalid
	}
	if err := validateDesktopPKCE(record.CodeChallenge, "S256", true, input.CodeVerifier); err != nil {
		return nil, ErrDesktopProofInvalid
	}
	canonicalKey, thumbprint, err := CanonicalDevicePublicKey(input.PublicKey)
	if err != nil || subtle.ConstantTimeCompare([]byte(thumbprint), []byte(record.PublicKeyThumbprint)) != 1 {
		return nil, ErrDesktopProofInvalid
	}

	user, err := s.users.GetByID(ctx, record.UserID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrDesktopAuthorizationDenied
		}
		return nil, ErrServiceUnavailable
	}
	if !user.IsActive() {
		return nil, ErrUserNotActive
	}
	if consumed, err := consumeDesktopAuthorizationScript.Run(ctx, s.redis, []string{key}).Int(); err != nil {
		return nil, ErrServiceUnavailable
	} else {
		switch consumed {
		case -1:
			return nil, ErrDesktopAuthorizationExpired
		case -2:
			return nil, ErrDesktopAuthorizationPending
		case -3:
			return nil, ErrDesktopAuthorizationDenied
		case -4:
			return nil, ErrDesktopAuthorizationUsed
		case 0:
			return nil, ErrDesktopAuthorizationExpired
		}
	}

	// The enrolled public-key thumbprint is the stable device identifier. This
	// keeps the JWT/refresh/session records cryptographically bound to the same
	// P-256 key that must sign every DPoP request; a separate random id could be
	// copied or accidentally detached from the key during re-enrollment.
	deviceID := record.PublicKeyThumbprint
	if strings.TrimSpace(deviceID) == "" {
		return nil, ErrDesktopProofInvalid
	}
	familyID, err := randomDesktopToken(desktopFamilyIDBytes)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	dpopNonce, err := randomDesktopToken(24)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	pair, err := s.auth.GenerateDeviceTokenPair(ctx, user, familyID, deviceID, record.Scopes, record.Audience)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	if err := s.insertDevice(ctx, deviceID, user.ID, record, familyID, canonicalKey, dpopNonce); err != nil {
		_ = s.auth.RevokeSessionFamily(ctx, familyID)
		if errors.Is(err, ErrDesktopDeviceAlreadyActive) || errors.Is(err, ErrDesktopDeviceKeyOwned) {
			return nil, err
		}
		return nil, ErrServiceUnavailable
	}
	device := &DesktopDevice{
		DeviceID:            deviceID,
		ClientID:            record.ClientID,
		DeviceName:          record.DeviceName,
		PublicKeyThumbprint: record.PublicKeyThumbprint,
		Scopes:              append([]string(nil), record.Scopes...),
		Audience:            record.Audience,
		ProtectionLevel:     record.ProtectionLevel,
		CreatedAt:           time.Now().UTC(),
		LastSeenAt:          time.Now().UTC(),
	}
	return &DesktopDeviceToken{TokenPair: *pair, TokenType: "DPoP", Scope: strings.Join(record.Scopes, " "), Audience: record.Audience, DPoPNonce: dpopNonce, Device: device}, nil
}

func (s *DesktopDeviceService) insertDevice(ctx context.Context, deviceID string, userID int64, record desktopAuthorizationRecord, familyID string, publicKeyJWK []byte, dpopNonce string) error {
	if s.db == nil {
		return ErrServiceUnavailable
	}
	scopes, err := json.Marshal(record.Scopes)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO desktop_devices
			(device_id, user_id, client_id, device_name, public_key_thumbprint, public_key_jwk, dpop_nonce, protection_level, scopes, audience, session_id, created_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9::jsonb, $10, $11, NOW(), NOW())
		ON CONFLICT (device_id) DO UPDATE SET
			user_id = EXCLUDED.user_id,
			client_id = EXCLUDED.client_id,
			device_name = EXCLUDED.device_name,
			public_key_thumbprint = EXCLUDED.public_key_thumbprint,
			public_key_jwk = EXCLUDED.public_key_jwk,
			dpop_nonce = EXCLUDED.dpop_nonce,
			protection_level = EXCLUDED.protection_level,
			scopes = EXCLUDED.scopes,
			audience = EXCLUDED.audience,
			session_id = EXCLUDED.session_id,
			created_at = NOW(),
			last_seen_at = NOW(),
			revoked_at = NULL
		WHERE desktop_devices.user_id = EXCLUDED.user_id
		  AND desktop_devices.revoked_at IS NOT NULL
	`, deviceID, userID, record.ClientID, record.DeviceName, record.PublicKeyThumbprint, string(publicKeyJWK), dpopNonce, normalizeProtectionLevel(record.ProtectionLevel), string(scopes), record.Audience, familyID)
	if err != nil {
		return err
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr == nil && affected > 0 {
		// Re-enrollment of a previously revoked key replaces the session family.
		// Remove the hot revocation marker when possible.  The marker is also
		// family-bound (rather than a device-wide boolean), so a revoke racing
		// this delete cannot invalidate the newly enrolled session: the
		// session-aware checker ignores a marker belonging to the old family.
		// Redis is only a cache here: the SQL upsert above is already committed
		// and is the durable enrollment authority.  Never return the DEL error,
		// because ExchangeAuthorization would revoke the brand-new refresh family
		// and leave an active database row that cannot be re-enrolled.  Retry once
		// with a detached, bounded context so a canceled request still gets a
		// chance to clear the stale marker.
		if s.redis != nil {
			markerKey := desktopRevokedKeyPrefix + deviceID
			if err := s.redis.Del(ctx, markerKey).Err(); err != nil {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), desktopRevocationCleanupTimeout)
				_ = s.redis.Del(cleanupCtx, markerKey).Err()
				cancel()
			}
		}
		return nil
	}

	// PostgreSQL reports zero affected rows when the conflict WHERE clause
	// declines an update. Read only the ownership/active bits to distinguish a
	// same-user active session from a key that belongs to another account. This
	// second query is outside the write statement but the conflict itself was
	// serialized by PostgreSQL, so a concurrent exchange cannot replace it.
	var existingUserID int64
	var revokedAt sql.NullTime
	lookupErr := s.db.QueryRowContext(ctx, `
		SELECT user_id, revoked_at
		FROM desktop_devices
		WHERE device_id = $1
	`, deviceID).Scan(&existingUserID, &revokedAt)
	if errors.Is(lookupErr, sql.ErrNoRows) {
		// A concurrent administrative delete can remove the row between the
		// conflict statement and this diagnostic lookup. Surface a retryable
		// service error rather than guessing at ownership.
		return ErrServiceUnavailable
	}
	if lookupErr != nil {
		return lookupErr
	}
	if existingUserID != userID {
		return ErrDesktopDeviceKeyOwned
	}
	if !revokedAt.Valid {
		return ErrDesktopDeviceAlreadyActive
	}
	return ErrServiceUnavailable
}

func (s *DesktopDeviceService) Logout(ctx context.Context, refreshToken string, proofArgs ...DesktopDeviceProofInput) error {
	if s == nil || s.auth == nil || s.auth.refreshTokenCache == nil {
		return ErrRefreshTokenInvalid
	}
	if !strings.HasPrefix(strings.TrimSpace(refreshToken), refreshTokenPrefix) {
		return ErrRefreshTokenInvalid
	}
	hash := hashToken(strings.TrimSpace(refreshToken))
	data, err := s.auth.refreshTokenCache.GetRefreshToken(ctx, hash)
	if err != nil {
		// Distinguish an absent/revoked token from a cache outage. Both paths
		// fail closed (nothing is revoked on an untrusted token), but returning
		// a retryable service error avoids making a healthy desktop session look
		// permanently invalid while Redis is unavailable.
		if errors.Is(err, ErrRefreshTokenNotFound) {
			return ErrRefreshTokenInvalid
		}
		return ErrServiceUnavailable
	}
	if data == nil || data.DeviceID == "" {
		return ErrRefreshTokenInvalid
	}
	// Logout is a state-changing device operation. Requiring a fresh DPoP proof
	// prevents a copied refresh token from silently revoking a live device and,
	// more importantly, keeps this endpoint consistent with the sender-
	// constrained refresh flow. Keep the variadic form source-compatible with
	// old in-process callers, but fail closed when no proof is supplied.
	if len(proofArgs) != 1 {
		return ErrDesktopLogoutProofInvalid
	}
	proof := proofArgs[0]
	if _, err := s.VerifyDeviceProof(ctx, data.DeviceID, proof.DPoPProof, proof.Method, proof.TargetURL, ""); err != nil {
		if errors.Is(err, ErrDesktopProofInvalid) {
			return ErrDesktopLogoutProofInvalid
		}
		return err
	}
	// Persist the device revocation first: the SQL row is the durable authority
	// consulted by access-token middleware after a Redis restart. Still attempt
	// to revoke the refresh family even when the DB write fails, so a partial
	// outage cannot leave a reusable refresh token alive. Return the first
	// concrete failure so callers retry and surface the degraded state.
	deviceErr := s.revokeDeviceBySession(ctx, data.UserID, data.DeviceID, data.FamilyID)
	familyErr := s.auth.RevokeSessionFamily(ctx, data.FamilyID)
	if deviceErr != nil && !errors.Is(deviceErr, ErrDesktopDeviceNotFound) {
		return deviceErr
	}
	if familyErr != nil {
		return ErrServiceUnavailable
	}
	return deviceErr
}

// Refresh rotates a desktop refresh token after validating its device-bound
// DPoP proof. It deliberately delegates token-version, user-status and replay
// checks to AuthService.RefreshTokenPair so browser and desktop sessions share
// the same rotation semantics.
func (s *DesktopDeviceService) Refresh(ctx context.Context, input DesktopDeviceRefreshInput) (*DesktopDeviceToken, error) {
	if s == nil || s.auth == nil || s.auth.refreshTokenCache == nil {
		return nil, ErrServiceUnavailable
	}
	if strings.TrimSpace(input.ClientID) != DesktopClientID {
		return nil, ErrDesktopClientInvalid
	}
	if strings.TrimSpace(input.Audience) != DesktopAudience {
		return nil, ErrDesktopAudienceInvalid
	}
	refreshToken := strings.TrimSpace(input.RefreshToken)
	if refreshToken == "" || !strings.HasPrefix(refreshToken, refreshTokenPrefix) {
		return nil, ErrRefreshTokenInvalid
	}
	hash := hashToken(refreshToken)
	data, err := s.auth.refreshTokenCache.GetRefreshToken(ctx, hash)
	if err != nil {
		if errors.Is(err, ErrRefreshTokenNotFound) {
			return nil, ErrRefreshTokenInvalid
		}
		return nil, ErrServiceUnavailable
	}
	if data == nil || strings.TrimSpace(data.DeviceID) == "" {
		return nil, ErrRefreshTokenInvalid
	}
	if data.Audience != "" && data.Audience != DesktopAudience {
		return nil, ErrDesktopAudienceInvalid
	}
	_, thumbprint, err := CanonicalDevicePublicKey(input.PublicKey)
	if err != nil {
		return nil, ErrDesktopProofInvalid
	}
	var enrolledThumbprint string
	var enrolledSessionID string
	if s.db == nil {
		return nil, ErrServiceUnavailable
	}
	if err := s.db.QueryRowContext(ctx, `SELECT public_key_thumbprint, session_id FROM desktop_devices WHERE device_id = $1`, data.DeviceID).Scan(&enrolledThumbprint, &enrolledSessionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrDesktopDeviceRevoked
		}
		return nil, ErrServiceUnavailable
	}
	if subtle.ConstantTimeCompare([]byte(thumbprint), []byte(enrolledThumbprint)) != 1 {
		return nil, ErrDesktopProofInvalid
	}
	if strings.TrimSpace(data.FamilyID) == "" || subtle.ConstantTimeCompare([]byte(data.FamilyID), []byte(enrolledSessionID)) != 1 {
		// A device public key can be re-enrolled after revocation. An old
		// refresh family must not rotate into the newly enrolled session.
		return nil, ErrRefreshTokenInvalid
	}
	if _, err := s.VerifyDeviceProof(ctx, data.DeviceID, input.DPoPProof, input.Method, input.TargetURL, ""); err != nil {
		return nil, err
	}
	pair, err := s.auth.RefreshTokenPair(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	device, err := s.deviceByID(ctx, data.DeviceID)
	if err != nil {
		return nil, err
	}
	return &DesktopDeviceToken{
		TokenPair: pair.TokenPair,
		TokenType: "DPoP",
		Scope:     strings.Join(data.Scopes, " "),
		Audience:  data.Audience,
		DPoPNonce: device.DPoPNonce,
		Device:    &device.DesktopDevice,
	}, nil
}

type desktopDeviceWithNonce struct {
	DesktopDevice
	DPoPNonce string
}

func (s *DesktopDeviceService) deviceByID(ctx context.Context, deviceID string) (desktopDeviceWithNonce, error) {
	var d desktopDeviceWithNonce
	var scopesRaw []byte
	var revoked sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT device_id, client_id, device_name, public_key_thumbprint, scopes, audience, protection_level, created_at, last_seen_at, revoked_at, dpop_nonce
		FROM desktop_devices WHERE device_id = $1`, deviceID).Scan(
		&d.DeviceID, &d.ClientID, &d.DeviceName, &d.PublicKeyThumbprint, &scopesRaw,
		&d.Audience, &d.ProtectionLevel, &d.CreatedAt, &d.LastSeenAt, &revoked, &d.DPoPNonce)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrDesktopDeviceRevoked
	}
	if err != nil {
		return d, ErrServiceUnavailable
	}
	if revoked.Valid {
		return d, ErrDesktopDeviceRevoked
	}
	if len(scopesRaw) > 0 {
		if err := json.Unmarshal(scopesRaw, &d.Scopes); err != nil {
			return d, ErrServiceUnavailable
		}
	}
	if d.Scopes == nil {
		d.Scopes = []string{}
	}
	d.ProtectionLevel = normalizeProtectionLevel(d.ProtectionLevel)
	return d, nil
}

func (s *DesktopDeviceService) revokeDeviceBySession(ctx context.Context, userID int64, deviceID, familyID string) error {
	if s.db == nil {
		return ErrServiceUnavailable
	}
	result, err := s.db.ExecContext(ctx, `UPDATE desktop_devices SET revoked_at = COALESCE(revoked_at, NOW()), last_seen_at = NOW() WHERE device_id = $1 AND user_id = $2 AND ($3 = '' OR session_id = $3)`, deviceID, userID, strings.TrimSpace(familyID))
	if err != nil {
		return ErrServiceUnavailable
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrDesktopDeviceNotFound
	}
	return s.markDeviceRevoked(ctx, deviceID, familyID)
}

func (s *DesktopDeviceService) ListDevices(ctx context.Context, userID int64) ([]DesktopDevice, error) {
	if s == nil || s.db == nil {
		return nil, ErrServiceUnavailable
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT device_id, client_id, device_name, public_key_thumbprint, scopes, audience, protection_level, created_at, last_seen_at, revoked_at
		FROM desktop_devices WHERE user_id = $1 ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	defer rows.Close()
	devices := make([]DesktopDevice, 0)
	for rows.Next() {
		var d DesktopDevice
		var scopesRaw []byte
		var revoked sql.NullTime
		if err := rows.Scan(&d.DeviceID, &d.ClientID, &d.DeviceName, &d.PublicKeyThumbprint, &scopesRaw, &d.Audience, &d.ProtectionLevel, &d.CreatedAt, &d.LastSeenAt, &revoked); err != nil {
			return nil, ErrServiceUnavailable
		}
		d.ProtectionLevel = normalizeProtectionLevel(d.ProtectionLevel)
		if len(scopesRaw) > 0 {
			if err := json.Unmarshal(scopesRaw, &d.Scopes); err != nil {
				return nil, ErrServiceUnavailable
			}
		}
		if d.Scopes == nil {
			d.Scopes = []string{}
		}
		if revoked.Valid {
			t := revoked.Time
			d.RevokedAt = &t
		}
		devices = append(devices, d)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrServiceUnavailable
	}
	return devices, nil
}

func (s *DesktopDeviceService) RevokeDevice(ctx context.Context, userID int64, deviceID string) error {
	if s == nil || s.db == nil || strings.TrimSpace(deviceID) == "" {
		return ErrDesktopDeviceNotFound
	}
	row := s.db.QueryRowContext(ctx, `UPDATE desktop_devices SET revoked_at = COALESCE(revoked_at, NOW()), last_seen_at = NOW() WHERE device_id = $1 AND user_id = $2 RETURNING session_id`, strings.TrimSpace(deviceID), userID)
	var familyID string
	if err := row.Scan(&familyID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDesktopDeviceNotFound
		}
		return ErrServiceUnavailable
	}
	if s.auth != nil && familyID != "" {
		if err := s.auth.RevokeSessionFamily(ctx, familyID); err != nil {
			return ErrServiceUnavailable
		}
	}
	return s.markDeviceRevoked(ctx, strings.TrimSpace(deviceID), familyID)
}

// RevokeDeviceForActor adds the sender binding to the account-scoped revoke
// operation. Browser sessions pass an empty actorDeviceID and retain the
// existing ability to revoke any device owned by the account. A desktop token
// must name its own enrolled device, preventing a stolen profile-scoped token
// from using the device-management endpoint as an account-wide DoS primitive.
func (s *DesktopDeviceService) RevokeDeviceForActor(ctx context.Context, userID int64, deviceID, actorDeviceID string) error {
	deviceID = strings.TrimSpace(deviceID)
	actorDeviceID = strings.TrimSpace(actorDeviceID)
	if actorDeviceID != "" && subtle.ConstantTimeCompare([]byte(deviceID), []byte(actorDeviceID)) != 1 {
		return ErrDesktopDeviceSelfOnly
	}
	return s.RevokeDevice(ctx, userID, deviceID)
}

// IsDeviceSessionRevoked first checks Redis for the hot path, then falls back
// to PostgreSQL so a Redis restart cannot resurrect a revoked access token.
func (s *DesktopDeviceService) IsDeviceSessionRevoked(ctx context.Context, deviceID string) (bool, error) {
	if strings.TrimSpace(deviceID) == "" {
		return false, nil
	}
	if s == nil {
		return true, ErrServiceUnavailable
	}
	if s.redis != nil {
		value, err := s.redis.Get(ctx, desktopRevokedKeyPrefix+deviceID).Result()
		if err == nil {
			// Generic callers do not present a session family. Any non-empty
			// marker therefore fails closed, including a family-bound marker
			// written by a concurrent revoke.
			return strings.TrimSpace(value) != "", nil
		}
		if err != redis.Nil {
			return true, err // fail closed for device tokens when Redis is unhealthy
		}
	}
	if s.db == nil {
		// A device token is valid only while its enrollment can be checked. Do
		// not treat a partially wired service or an unavailable database as an
		// active session; JWT middleware will fail closed for this error.
		return true, ErrServiceUnavailable
	}
	var revoked sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT revoked_at FROM desktop_devices WHERE device_id = $1`, deviceID).Scan(&revoked)
	if errors.Is(err, sql.ErrNoRows) {
		// A desktop access token is valid only while its enrolled device row
		// exists.  Treat a physically deleted row as revoked; returning false
		// here would let a token survive an administrative hard-delete or a
		// partially applied migration until its ten-minute JWT expiry.
		return true, nil
	}
	if err != nil {
		return true, err
	}
	if revoked.Valid {
		if s.redis != nil {
			_ = s.redis.Set(ctx, desktopRevokedKeyPrefix+deviceID, "1", desktopRevocationTTL).Err()
		}
		return true, nil
	}
	return false, nil
}

// IsDeviceSessionRevokedForSession is the access-token check used by the JWT
// middleware.  In addition to the durable revoked_at flag it requires the JWT
// sid claim to match the currently enrolled refresh-token family.  Rebinding a
// previously revoked public key therefore invalidates every old access token,
// even though the stable device id is intentionally derived from that key.
func (s *DesktopDeviceService) IsDeviceSessionRevokedForSession(ctx context.Context, deviceID, sessionID string) (bool, error) {
	deviceID = strings.TrimSpace(deviceID)
	sessionID = strings.TrimSpace(sessionID)
	if deviceID == "" || sessionID == "" {
		return true, nil
	}
	if s == nil {
		return true, ErrServiceUnavailable
	}
	// Once a public key is re-enrolled, the SQL row is the durable authority for
	// the new family.  Read it before the Redis marker: an old legacy "1"
	// marker (or a marker for the previous family) may remain when cache cleanup
	// races the upsert.  Treating that stale marker as device-wide would revoke
	// the freshly issued session and recreate the enrollment lockout this method
	// is intended to prevent.
	if s.db != nil {
		var enrolledSession string
		var revokedAt sql.NullTime
		err := s.db.QueryRowContext(ctx, `SELECT session_id, revoked_at FROM desktop_devices WHERE device_id = $1`, deviceID).Scan(&enrolledSession, &revokedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return true, nil
		}
		if err != nil {
			return true, err
		}
		if revokedAt.Valid || strings.TrimSpace(enrolledSession) == "" {
			return true, nil
		}
		if subtle.ConstantTimeCompare([]byte(enrolledSession), []byte(sessionID)) != 1 {
			return true, nil
		}
		if s.redis == nil {
			return false, nil
		}
		value, redisErr := s.redis.Get(ctx, desktopRevokedKeyPrefix+deviceID).Result()
		if redisErr == redis.Nil {
			return false, nil
		}
		if redisErr != nil {
			return true, redisErr
		}
		marker := strings.TrimSpace(value)
		if marker != "" && marker != "1" && subtle.ConstantTimeCompare([]byte(marker), []byte(sessionID)) == 1 {
			return true, nil
		}
		// "1" and a marker for another family are stale relative to the active
		// SQL row.  A later bounded cleanup removes them; ignoring them here is
		// safe because a current-family revoke first sets revoked_at durably.
		return false, nil
	}

	// Focused tests and compatibility embedders may provide only Redis. Retain
	// the conservative legacy behavior when no durable enrollment store exists.
	if s.redis != nil {
		value, err := s.redis.Get(ctx, desktopRevokedKeyPrefix+deviceID).Result()
		if err == redis.Nil {
			return false, nil
		}
		if err != nil {
			return true, err
		}
		marker := strings.TrimSpace(value)
		return marker == "1" || (marker != "" && subtle.ConstantTimeCompare([]byte(marker), []byte(sessionID)) == 1), nil
	}
	return true, ErrServiceUnavailable
}

// VerifyDeviceProof validates an RFC 9449-style DPoP proof for a desktop
// access token.  The proof carries the public JWK, request binding, nonce and
// access-token hash; the server compares its thumbprint with the enrolled
// device before accepting the ECDSA signature.  A jti is atomically consumed
// in Redis so a captured proof cannot be replayed.
func (s *DesktopDeviceService) VerifyDeviceProof(ctx context.Context, deviceID, proof, method, targetURL, accessToken string) (string, error) {
	deviceID = strings.TrimSpace(deviceID)
	if s == nil || s.db == nil || s.redis == nil || deviceID == "" || len(deviceID) > 128 {
		return "", ErrServiceUnavailable
	}
	// The OAuth refresh-token grant is itself proof-bound but has no access
	// token yet, so its DPoP proof intentionally omits `ath`. Normal API
	// requests must still provide a non-empty bearer token and are checked
	// against `ath` below.
	if strings.TrimSpace(proof) == "" || (strings.TrimSpace(accessToken) == "" && !isDesktopRefreshProofRequest(method, targetURL)) {
		return "", ErrDesktopProofInvalid
	}
	var jwkRaw []byte
	var nonce string
	var revoked sql.NullTime
	if err := s.db.QueryRowContext(ctx, `
		SELECT public_key_jwk, dpop_nonce, revoked_at
		FROM desktop_devices WHERE device_id = $1`, deviceID).Scan(&jwkRaw, &nonce, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrDesktopDeviceRevoked
		}
		return "", ErrServiceUnavailable
	}
	if revoked.Valid {
		return nonce, ErrDesktopDeviceRevoked
	}
	if strings.TrimSpace(nonce) == "" || len(jwkRaw) == 0 {
		return nonce, ErrDesktopProofInvalid
	}
	proofJWK, payload, signature, signingInput, err := parseDPoPProof(proof)
	if err != nil {
		return nonce, ErrDesktopProofInvalid
	}
	_, enrolledThumbprint, err := CanonicalDevicePublicKey(jwkRaw)
	if err != nil {
		return nonce, ErrDesktopProofInvalid
	}
	_, proofThumbprint, err := CanonicalDevicePublicKey(proofJWK)
	if err != nil || subtle.ConstantTimeCompare([]byte(enrolledThumbprint), []byte(proofThumbprint)) != 1 {
		return nonce, ErrDesktopProofInvalid
	}
	pub, err := publicKeyFromJWK(proofJWK)
	if err != nil || !verifyDPoPSignature(pub, signingInput, signature) {
		return nonce, ErrDesktopProofInvalid
	}
	if !strings.EqualFold(strings.TrimSpace(payload.HTTPMethod), strings.TrimSpace(method)) {
		return nonce, ErrDesktopProofInvalid
	}
	wantURL, err := canonicalDPoPURL(targetURL)
	if err != nil {
		return nonce, ErrDesktopProofInvalid
	}
	gotURL, err := canonicalDPoPURL(payload.HTTPURI)
	if err != nil || gotURL != wantURL {
		return nonce, ErrDesktopProofInvalid
	}
	now := time.Now().Unix()
	if payload.IAT <= 0 || payload.IAT < now-int64(dpopClockSkew/time.Second) || payload.IAT > now+int64(dpopClockSkew/time.Second) {
		return nonce, ErrDesktopProofInvalid
	}
	if subtle.ConstantTimeCompare([]byte(payload.Nonce), []byte(nonce)) != 1 || len(payload.JTI) < 8 || len(payload.JTI) > 128 {
		return nonce, ErrDesktopProofInvalid
	}
	if strings.TrimSpace(accessToken) != "" {
		accessHash := sha256.Sum256([]byte(accessToken))
		if subtle.ConstantTimeCompare([]byte(payload.AccessTokenHash), []byte(base64.RawURLEncoding.EncodeToString(accessHash[:]))) != 1 {
			return nonce, ErrDesktopProofInvalid
		}
	}
	replayKey := desktopDPoPReplayPrefix + deviceID + ":" + payload.JTI
	accepted, err := s.redis.SetNX(ctx, replayKey, "1", dpopReplayTTL).Result()
	if err != nil {
		return nonce, ErrServiceUnavailable
	}
	if !accepted {
		return nonce, ErrDesktopProofInvalid
	}
	// Device activity is informational; a failed best-effort update must not
	// turn an otherwise valid request into a 5xx response.
	_, _ = s.db.ExecContext(ctx, `UPDATE desktop_devices SET last_seen_at = NOW() WHERE device_id = $1 AND revoked_at IS NULL`, deviceID)
	return nonce, nil
}

func isDesktopRefreshProofRequest(method, targetURL string) bool {
	if !strings.EqualFold(strings.TrimSpace(method), http.MethodPost) {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(targetURL))
	if err != nil {
		return false
	}
	path := strings.TrimRight(u.Path, "/")
	return strings.HasSuffix(path, "/api/v1/desktop/token") ||
		strings.HasSuffix(path, "/api/v1/auth/device/token") ||
		strings.HasSuffix(path, "/api/v1/desktop/logout") ||
		strings.HasSuffix(path, "/api/v1/auth/device/logout")
}

const desktopDPoPReplayPrefix = "desktop:dpop:jti:"

type dpopProofPayload struct {
	HTTPURI         string `json:"htu"`
	HTTPMethod      string `json:"htm"`
	IAT             int64  `json:"iat"`
	JTI             string `json:"jti"`
	Nonce           string `json:"nonce"`
	AccessTokenHash string `json:"ath"`
}

func parseDPoPProof(raw string) (json.RawMessage, dpopProofPayload, []byte, []byte, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || len(parts[0]) > 8192 || len(parts[1]) > 16384 || len(parts[2]) > 1024 {
		return nil, dpopProofPayload{}, nil, nil, errors.New("invalid dpop compact serialization")
	}
	headerBytes, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		return nil, dpopProofPayload{}, nil, nil, err
	}
	payloadBytes, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		return nil, dpopProofPayload{}, nil, nil, err
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil {
		return nil, dpopProofPayload{}, nil, nil, err
	}
	var header struct {
		Type string          `json:"typ"`
		Alg  string          `json:"alg"`
		JWK  json.RawMessage `json:"jwk"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil || !strings.EqualFold(header.Type, "dpop+jwt") || header.Alg != "ES256" || len(header.JWK) == 0 {
		return nil, dpopProofPayload{}, nil, nil, errors.New("invalid dpop header")
	}
	var payload dpopProofPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, dpopProofPayload{}, nil, nil, err
	}
	return append(json.RawMessage(nil), header.JWK...), payload, sig, []byte(parts[0] + "." + parts[1]), nil
}

func publicKeyFromJWK(raw json.RawMessage) (*ecdsa.PublicKey, error) {
	var key struct {
		KTY string `json:"kty"`
		CRV string `json:"crv"`
		X   string `json:"x"`
		Y   string `json:"y"`
	}
	if err := json.Unmarshal(raw, &key); err != nil || key.KTY != "EC" || key.CRV != "P-256" {
		return nil, ErrDesktopPublicKeyInvalid
	}
	xBytes, err := base64.RawURLEncoding.Strict().DecodeString(key.X)
	if err != nil {
		return nil, err
	}
	yBytes, err := base64.RawURLEncoding.Strict().DecodeString(key.Y)
	if err != nil {
		return nil, err
	}
	if len(xBytes) != 32 || len(yBytes) != 32 {
		return nil, ErrDesktopPublicKeyInvalid
	}
	x, y := new(big.Int).SetBytes(xBytes), new(big.Int).SetBytes(yBytes)
	if !elliptic.P256().IsOnCurve(x, y) {
		return nil, ErrDesktopPublicKeyInvalid
	}
	return &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, nil
}

func verifyDPoPSignature(pub *ecdsa.PublicKey, input, signature []byte) bool {
	if pub == nil || len(signature) != 64 {
		return false
	}
	sum := sha256.Sum256(input)
	// JOSE ES256 uses fixed-width, raw R || S.  Do not accept ASN.1 DER here:
	// accepting two encodings for the same proof needlessly widens parser
	// behavior and can create verification/signature-normalization gaps between
	// desktop implementations and intermediaries.
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])
	return ecdsa.Verify(pub, sum[:], r, s)
}

func canonicalDPoPURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid dpop URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	// Device sessions are proof-bound credentials. Accepting an http htu would
	// allow a bearer-equivalent cleartext hop (or a misconfigured proxy) to
	// replay the access token and proof on the wire. Browser/non-device flows do
	// not call this canonicalizer.
	if u.Scheme != "https" {
		return "", errors.New("invalid dpop URL scheme")
	}
	if u.Path == "" {
		u.Path = "/"
	}
	u.RawPath = ""
	return u.String(), nil
}

func (s *DesktopDeviceService) markDeviceRevoked(ctx context.Context, deviceID, sessionID string) error {
	if s.redis == nil {
		return nil
	}
	marker := strings.TrimSpace(sessionID)
	if marker == "" {
		// If the durable row did not expose a family, retain the legacy
		// device-wide marker and fail closed rather than creating an
		// unscoped value that a session-aware checker could accidentally ignore.
		marker = "1"
	}
	return s.redis.Set(ctx, desktopRevokedKeyPrefix+deviceID, marker, desktopRevocationTTL).Err()
}

// NormalizeDesktopScopes validates and canonicalizes the user-approved scope
// set. Unknown scopes are rejected instead of silently escalating a future API.
func NormalizeDesktopScopes(scopes []string) ([]string, error) {
	allowed := map[string]struct{}{
		"openid": {}, "profile": {}, "balance": {}, "usage": {},
		"billing": {}, "checkin": {}, "images": {}, "api_keys": {},
	}
	seen := make(map[string]struct{}, len(scopes))
	for _, raw := range scopes {
		for _, item := range strings.Fields(strings.TrimSpace(raw)) {
			item = strings.ToLower(item)
			if _, ok := allowed[item]; !ok {
				return nil, ErrDesktopInvalidRequest
			}
			seen[item] = struct{}{}
		}
	}
	if len(seen) == 0 {
		seen["openid"] = struct{}{}
		seen["profile"] = struct{}{}
		seen["balance"] = struct{}{}
		seen["usage"] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for item := range seen {
		out = append(out, item)
	}
	sort.Strings(out)
	return out, nil
}

// DevicePublicKeyThumbprint validates a public JWK and computes a canonical
// thumbprint. Private key members are rejected so clients cannot accidentally
// upload secret material into Redis or logs.
func DevicePublicKeyThumbprint(raw json.RawMessage) (string, error) {
	_, thumbprint, err := CanonicalDevicePublicKey(raw)
	return thumbprint, err
}

// CanonicalDevicePublicKey validates a public JWK and returns the RFC 7638
// canonical JSON alongside its SHA-256 thumbprint. Only an EC P-256 key is
// accepted; private members are rejected before anything is persisted.
func CanonicalDevicePublicKey(raw json.RawMessage) ([]byte, string, error) {
	if len(raw) == 0 || len(raw) > maxDesktopPublicKeyBytes {
		return nil, "", ErrDesktopPublicKeyRequired
	}
	var key map[string]any
	if err := json.Unmarshal(raw, &key); err != nil {
		return nil, "", ErrDesktopPublicKeyInvalid
	}
	if len(key) == 0 {
		return nil, "", ErrDesktopPublicKeyInvalid
	}
	for _, private := range []string{"d", "p", "q", "dp", "dq", "qi", "oth", "k"} {
		if _, exists := key[private]; exists {
			return nil, "", ErrDesktopPublicKeyInvalid
		}
	}
	kty, _ := key["kty"].(string)
	canonical := map[string]string{"kty": kty}
	switch kty {
	case "EC":
		crv, crvOK := key["crv"].(string)
		x, xOK := key["x"].(string)
		y, yOK := key["y"].(string)
		// P-256 is the only key type currently accepted. Restricting the curve
		// keeps verification interoperable across Windows/macOS and avoids
		// silently accepting keys for which a future DPoP verifier has no policy.
		if !crvOK || crv != "P-256" || !xOK || !yOK || x == "" || y == "" {
			return nil, "", ErrDesktopPublicKeyInvalid
		}
		// RFC 7638 thumbprints require the canonical base64url spelling. The
		// strict decoder rejects non-zero unused tail bits; accepting those
		// aliases would let one EC point register under multiple device IDs and
		// undermine the cross-account public-key ownership check.
		xBytes, xErr := base64.RawURLEncoding.Strict().DecodeString(x)
		yBytes, yErr := base64.RawURLEncoding.Strict().DecodeString(y)
		if xErr != nil || yErr != nil || len(xBytes) != 32 || len(yBytes) != 32 {
			return nil, "", ErrDesktopPublicKeyInvalid
		}
		if !elliptic.P256().IsOnCurve(new(big.Int).SetBytes(xBytes), new(big.Int).SetBytes(yBytes)) {
			return nil, "", ErrDesktopPublicKeyInvalid
		}
		canonical["crv"], canonical["x"], canonical["y"] = crv, x, y
	default:
		return nil, "", ErrDesktopPublicKeyInvalid
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, "", ErrDesktopPublicKeyInvalid
	}
	sum := sha256.Sum256(encoded)
	return encoded, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func normalizeProtectionLevel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "hardware", "secure_enclave", "tpm", "cng":
		return "hardware"
	case "keychain", "credential_manager", "secret_service", "os":
		return "os"
	default:
		return "software"
	}
}

func normalizeRequestedProtectionLevel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "keychain", "credential_manager", "secret_service", "os":
		return "os"
	case "software", "memory", "":
		return "software"
	default:
		return "software"
	}
}

func validateDesktopPKCE(challenge, method string, verify bool, verifier ...string) error {
	// RFC 7636 requires an S256 challenge encoded as unpadded base64url of the
	// SHA-256 digest (43 ASCII characters). Do not trim or normalize it: doing
	// so would accept a different value than the one the client actually bound
	// to its authorization request.
	if method != "S256" || !isRFC7636Challenge(challenge) {
		if verify {
			return ErrDesktopProofInvalid
		}
		return ErrDesktopPKCERequired
	}
	if !verify {
		return nil
	}
	if len(verifier) == 0 || !isRFC7636Verifier(verifier[0]) {
		return ErrDesktopProofInvalid
	}
	sum := sha256.Sum256([]byte(verifier[0]))
	actual := base64.RawURLEncoding.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(actual), []byte(challenge)) != 1 {
		return ErrDesktopProofInvalid
	}
	return nil
}

// RFC 7636's code_verifier ABNF is 43-128 ASCII unreserved characters. Keep
// this check byte-oriented so UTF-8 or other Unicode lookalikes cannot pass a
// length check and be normalized differently by another implementation.
func isRFC7636Verifier(value string) bool {
	if len(value) < 43 || len(value) > maxDesktopCodeVerifierLen {
		return false
	}
	for i := 0; i < len(value); i++ {
		if !isRFC7636Unreserved(value[i]) {
			return false
		}
	}
	return true
}

func isRFC7636Challenge(value string) bool {
	// SHA-256's unpadded base64url representation is exactly 43 characters.
	if len(value) != 43 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if !isRFC7636Unreserved(value[i]) {
			return false
		}
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func isRFC7636Unreserved(value byte) bool {
	return (value >= 'A' && value <= 'Z') ||
		(value >= 'a' && value <= 'z') ||
		(value >= '0' && value <= '9') ||
		value == '-' || value == '.' || value == '_' || value == '~'
}

func randomDesktopToken(bytesLen int) (string, error) {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func hashDesktopCode(value string) string {
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), "-", ""))))
	return hex.EncodeToString(sum[:])
}

func formatDesktopUserCode(raw string) string {
	raw = strings.ToUpper(raw)
	if len(raw) <= 4 {
		return raw
	}
	return raw[:4] + "-" + raw[4:]
}

func parseUnix(value string) time.Time {
	seconds, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return time.Unix(seconds, 0)
}

func decodeDesktopAuthorizationRecord(values map[string]string) (desktopAuthorizationRecord, error) {
	record := desktopAuthorizationRecord{
		Status:              values["status"],
		ClientID:            values["client_id"],
		DeviceName:          values["device_name"],
		PublicKeyThumbprint: values["public_key_thumbprint"],
		CodeChallenge:       values["code_challenge"],
		CodeChallengeMethod: values["code_challenge_method"],
		Audience:            values["audience"],
		ProtectionLevel:     normalizeProtectionLevel(values["protection_level"]),
	}
	if raw := strings.TrimSpace(values["public_key_jwk"]); raw != "" {
		record.PublicKeyJWK = json.RawMessage(raw)
	}
	record.Scopes = strings.Fields(values["scopes"])
	if record.Status == "" || record.ClientID == "" || record.PublicKeyThumbprint == "" || record.CodeChallenge == "" || record.Audience == "" {
		return record, fmt.Errorf("incomplete desktop authorization record")
	}
	if raw := strings.TrimSpace(values["user_id"]); raw != "" {
		record.UserID, _ = strconv.ParseInt(raw, 10, 64)
	}
	return record, nil
}

var approveDesktopAuthorizationScript = redis.NewScript(`
local key = KEYS[1]
if redis.call('EXISTS', key) == 0 then return -1 end
local current = redis.call('HGET', key, 'status')
if current ~= 'pending' then return -2 end
if ARGV[1] == 'approved' then
  if ARGV[3] == nil or ARGV[3] == '' then return -3 end
  local requested = {}
  for scope in string.gmatch(redis.call('HGET', key, 'scopes') or '', '%S+') do
    requested[scope] = true
  end
  for scope in string.gmatch(ARGV[3], '%S+') do
    if not requested[scope] then return -3 end
  end
  redis.call('HSET', key, 'status', ARGV[1], 'user_id', ARGV[2], 'scopes', ARGV[3])
else
  redis.call('HSET', key, 'status', ARGV[1], 'user_id', ARGV[2])
end
return 1
`)

var consumeDesktopAuthorizationScript = redis.NewScript(`
local key = KEYS[1]
if redis.call('EXISTS', key) == 0 then return -1 end
local current = redis.call('HGET', key, 'status')
if current == 'pending' then return -2 end
if current == 'denied' then return -3 end
if current == 'consumed' then return -4 end
if current ~= 'approved' then return 0 end
redis.call('HSET', key, 'status', 'consumed')
return 1
`)
