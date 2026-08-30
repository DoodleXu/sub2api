package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type desktopUserRepoStub struct {
	UserRepository
	user *User
}

func (s *desktopUserRepoStub) GetByID(context.Context, int64) (*User, error) { return s.user, nil }

type desktopRefreshCacheStub struct {
	RefreshTokenCache
	values map[string]*RefreshTokenData
}

type desktopRefreshCacheErrorStub struct {
	desktopRefreshCacheStub
	err error
}

func (s *desktopRefreshCacheErrorStub) GetRefreshToken(context.Context, string) (*RefreshTokenData, error) {
	return nil, s.err
}

type desktopRefreshCacheFamilyErrorStub struct {
	desktopRefreshCacheStub
	err error
}

func (s *desktopRefreshCacheFamilyErrorStub) DeleteTokenFamily(context.Context, string) error {
	return s.err
}

func (s *desktopRefreshCacheStub) StoreRefreshToken(_ context.Context, hash string, data *RefreshTokenData, _ time.Duration) error {
	if s.values == nil {
		s.values = map[string]*RefreshTokenData{}
	}
	s.values[hash] = data
	return nil
}
func (s *desktopRefreshCacheStub) GetRefreshToken(_ context.Context, hash string) (*RefreshTokenData, error) {
	if value, ok := s.values[hash]; ok {
		return value, nil
	}
	return nil, ErrRefreshTokenNotFound
}
func (s *desktopRefreshCacheStub) DeleteRefreshToken(context.Context, string) error     { return nil }
func (s *desktopRefreshCacheStub) DeleteUserRefreshTokens(context.Context, int64) error { return nil }
func (s *desktopRefreshCacheStub) DeleteTokenFamily(context.Context, string) error      { return nil }
func (s *desktopRefreshCacheStub) AddToUserTokenSet(context.Context, int64, string, time.Duration) error {
	return nil
}
func (s *desktopRefreshCacheStub) AddToFamilyTokenSet(context.Context, string, string, time.Duration) error {
	return nil
}
func (s *desktopRefreshCacheStub) GetUserTokenHashes(context.Context, int64) ([]string, error) {
	return nil, nil
}
func (s *desktopRefreshCacheStub) GetFamilyTokenHashes(context.Context, string) ([]string, error) {
	return nil, nil
}
func (s *desktopRefreshCacheStub) IsTokenInFamily(context.Context, string, string) (bool, error) {
	return true, nil
}

func p256JWK(t *testing.T) (json.RawMessage, string) {
	t.Helper()
	private, x, y, err := elliptic.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	_ = private
	enc := base64.RawURLEncoding
	pad := func(value []byte) []byte {
		out := make([]byte, 32)
		copy(out[32-len(value):], value)
		return out
	}
	raw := json.RawMessage(`{"kty":"EC","crv":"P-256","x":"` + enc.EncodeToString(pad(x.Bytes())) + `","y":"` + enc.EncodeToString(pad(y.Bytes())) + `"}`)
	thumbprint, err := DevicePublicKeyThumbprint(raw)
	require.NoError(t, err)
	return raw, thumbprint
}

func TestDevicePublicKeyThumbprintRejectsNonP256AndPrivateMaterial(t *testing.T) {
	_, err := DevicePublicKeyThumbprint(json.RawMessage(`{"kty":"EC","crv":"secp256k1","x":"AA","y":"AA"}`))
	require.ErrorIs(t, err, ErrDesktopPublicKeyInvalid)
	_, err = DevicePublicKeyThumbprint(json.RawMessage(`{"kty":"EC","crv":"P-256","x":"AA","y":"AA","d":"secret"}`))
	require.ErrorIs(t, err, ErrDesktopPublicKeyInvalid)
}

func TestDevicePublicKeyThumbprintRejectsNonCanonicalBase64URLTailBits(t *testing.T) {
	raw, _ := p256JWK(t)
	var key map[string]string
	require.NoError(t, json.Unmarshal(raw, &key))
	// A 32-byte value encodes to 43 unpadded base64url characters. The last
	// character has two unused bits which must be zero in RFC 7638 input.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	for _, field := range []string{"x", "y"} {
		value := key[field]
		require.Len(t, value, 43)
		index := strings.IndexByte(alphabet, value[len(value)-1])
		require.NotEqual(t, -1, index)
		require.Equal(t, 0, index&0x03)
		key[field] = value[:len(value)-1] + string(alphabet[index+1])
		_, err := DevicePublicKeyThumbprint(mustDesktopJSON(t, key))
		require.ErrorIs(t, err, ErrDesktopPublicKeyInvalid, field)
		key[field] = value
	}
}

func mustDesktopJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}

func TestDesktopPKCEEnforcesRFC7636ASCIIAndCanonicalChallenge(t *testing.T) {
	verifier := strings.Repeat("v", 64)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	require.NoError(t, validateDesktopPKCE(challenge, "S256", false))

	// Whitespace, Unicode and punctuation outside the RFC 7636 unreserved set
	// must not be silently trimmed or accepted. Dot is explicitly allowed for
	// the verifier, so cover it as a positive case separately.
	dotVerifier := verifier[:10] + "." + verifier[11:]
	dotSum := sha256.Sum256([]byte(dotVerifier))
	dotChallenge := base64.RawURLEncoding.EncodeToString(dotSum[:])
	require.NoError(t, validateDesktopPKCE(dotChallenge, "S256", true, dotVerifier))
	for _, invalidVerifier := range []string{
		" " + verifier,
		verifier[:10] + "+" + verifier[11:],
		verifier[:10] + "中" + verifier[11:],
	} {
		require.ErrorIs(t, validateDesktopPKCE(challenge, "S256", true, invalidVerifier), ErrDesktopProofInvalid)
	}

	// S256 challenges are exactly 43 canonical base64url bytes; padding and
	// non-zero tail bits are rejected.
	require.ErrorIs(t, validateDesktopPKCE(challenge+"=", "S256", false), ErrDesktopPKCERequired)
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, challenge[42])
	require.NotEqual(t, -1, last)
	require.Equal(t, 0, last&0x03)
	nonCanonical := challenge[:42] + string(alphabet[last+1])
	require.ErrorIs(t, validateDesktopPKCE(nonCanonical, "S256", false), ErrDesktopPKCERequired)
	require.ErrorIs(t, validateDesktopPKCE(challenge[:10]+"."+challenge[11:], "S256", false), ErrDesktopPKCERequired)
}

func TestDesktopAuthorizationPKCEAndOneTimeExchange(t *testing.T) {
	mr := miniredis.NewMiniRedis()
	if err := mr.Start(); err != nil {
		t.Skipf("miniredis unavailable in restricted environment: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	user := &User{ID: 42, Email: "desktop@example.com", Role: RoleUser, Status: StatusActive, TokenVersion: 1, TokenVersionResolved: true}
	users := &desktopUserRepoStub{user: user}
	cache := &desktopRefreshCacheStub{values: map[string]*RefreshTokenData{}}
	auth := &AuthService{
		userRepo:          users,
		refreshTokenCache: cache,
		cfg:               &config.Config{JWT: config.JWTConfig{Secret: "test-secret", AccessTokenExpireMinutes: 60, RefreshTokenExpireDays: 30}},
	}
	svc := NewDesktopDeviceServiceWithStore(db, rdb, auth, users)
	publicKey, thumbprint := p256JWK(t)
	verifier := strings.Repeat("v", 64)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	authz, err := svc.CreateAuthorization(context.Background(), DesktopDeviceAuthorizationInput{
		ClientID:            DesktopClientID,
		DeviceName:          "Test Mac",
		PublicKey:           publicKey,
		CodeChallenge:       challenge,
		CodeChallengeMethod: "S256",
		Scopes:              []string{"profile", "usage"},
		Audience:            DesktopAudience,
	})
	require.NoError(t, err)
	require.Equal(t, int(desktopAuthorizationTTL/time.Second), authz.ExpiresIn)
	require.Equal(t, DesktopAuthorizationExpiresInSeconds, authz.ExpiresIn)
	require.Equal(t, DesktopPublicOrigin+"/device", authz.VerificationURI)
	require.Equal(t, DesktopPublicOrigin+"/device?user_code="+url.QueryEscape(authz.UserCode), authz.VerificationURIComplete)
	require.Len(t, authz.UserCode, 11)
	require.Equal(t, "-", authz.UserCode[4:5])
	require.NoError(t, svc.ApproveAuthorization(context.Background(), user.ID, authz.UserCode, true, []string{"profile", "usage"}))
	_, err = svc.ExchangeAuthorization(context.Background(), DesktopDeviceTokenInput{
		DeviceCode: authz.DeviceCode, CodeVerifier: verifier, PublicKey: publicKey, Audience: DesktopAudience,
	})
	require.ErrorIs(t, err, ErrDesktopClientInvalid)
	_, err = svc.ExchangeAuthorization(context.Background(), DesktopDeviceTokenInput{
		DeviceCode: authz.DeviceCode, CodeVerifier: verifier, PublicKey: publicKey, ClientID: DesktopClientID, Audience: "other-audience",
	})
	require.ErrorIs(t, err, ErrDesktopAudienceInvalid)
	mock.ExpectExec("INSERT INTO desktop_devices").WillReturnResult(sqlmock.NewResult(1, 1))
	token, err := svc.ExchangeAuthorization(context.Background(), DesktopDeviceTokenInput{
		DeviceCode:   authz.DeviceCode,
		CodeVerifier: verifier,
		PublicKey:    publicKey,
		ClientID:     DesktopClientID,
		Audience:     DesktopAudience,
	})
	require.NoError(t, err)
	require.Equal(t, int(desktopAccessTokenTTL/time.Second), token.ExpiresIn)
	require.NotNil(t, token.Device)
	require.Equal(t, thumbprint, token.Device.DeviceID)
	require.Equal(t, thumbprint, token.Device.PublicKeyThumbprint)
	claims, err := auth.ValidateToken(token.AccessToken)
	require.NoError(t, err)
	require.Equal(t, thumbprint, claims.DeviceID)
	require.Equal(t, DesktopAudience, claims.Audience[0])
	require.ElementsMatch(t, []string{"profile", "usage"}, claims.Scopes)
	_, err = svc.ExchangeAuthorization(context.Background(), DesktopDeviceTokenInput{DeviceCode: authz.DeviceCode, CodeVerifier: verifier, PublicKey: publicKey, ClientID: DesktopClientID, Audience: DesktopAudience})
	require.ErrorIs(t, err, ErrDesktopAuthorizationUsed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDesktopAuthorizationDowngradesUnauthenticatedHardwareClaim(t *testing.T) {
	mr := miniredis.NewMiniRedis()
	if err := mr.Start(); err != nil {
		t.Skipf("miniredis unavailable in restricted environment: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	svc := NewDesktopDeviceServiceWithStore(nil, rdb, nil, nil)
	publicKey, _ := p256JWK(t)
	verifier := strings.Repeat("v", 64)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	authz, err := svc.CreateAuthorization(context.Background(), DesktopDeviceAuthorizationInput{
		ClientID: DesktopClientID, DeviceName: "Hardware claim", PublicKey: publicKey,
		CodeChallenge: challenge, CodeChallengeMethod: "S256", Audience: DesktopAudience,
		ProtectionLevel: "hardware",
	})
	require.NoError(t, err)
	values, err := rdb.HGetAll(context.Background(), desktopAuthKeyPrefix+hashDesktopCode(authz.DeviceCode)).Result()
	require.NoError(t, err)
	require.Equal(t, "software", values["protection_level"])
}

func TestDesktopApprovalShowsRequestAndAtomicallyRestrictsScopes(t *testing.T) {
	mr := miniredis.NewMiniRedis()
	if err := mr.Start(); err != nil {
		t.Skipf("miniredis unavailable in restricted environment: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	svc := NewDesktopDeviceServiceWithStore(nil, rdb, nil, nil)
	publicKey, _ := p256JWK(t)
	verifier := strings.Repeat("v", 64)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	authz, err := svc.CreateAuthorization(context.Background(), DesktopDeviceAuthorizationInput{
		ClientID:            DesktopClientID,
		DeviceName:          "Consent Mac",
		PublicKey:           publicKey,
		CodeChallenge:       challenge,
		CodeChallengeMethod: "S256",
		Scopes:              []string{"profile", "usage", "billing", "api_keys"},
		Audience:            DesktopAudience,
	})
	require.NoError(t, err)
	details, err := svc.GetAuthorizationForApproval(context.Background(), authz.UserCode)
	require.NoError(t, err)
	require.Equal(t, DesktopClientID, details.ClientID)
	require.Equal(t, "Consent Mac", details.DeviceName)
	require.ElementsMatch(t, []string{"profile", "usage", "billing", "api_keys"}, details.Scopes)
	require.NoError(t, svc.ApproveAuthorization(context.Background(), 42, authz.UserCode, true, []string{"usage", "profile"}))
	deviceHash := hashDesktopCode(authz.DeviceCode)
	values, err := rdb.HGetAll(context.Background(), desktopAuthKeyPrefix+deviceHash).Result()
	require.NoError(t, err)
	require.Equal(t, "approved", values["status"])
	require.Equal(t, "profile usage", values["scopes"])

	// A stale or tampered browser cannot add a scope outside the original
	// request; rejection leaves the pending record untouched.
	authz2, err := svc.CreateAuthorization(context.Background(), DesktopDeviceAuthorizationInput{
		ClientID:            DesktopClientID,
		DeviceName:          "Consent Mac 2",
		PublicKey:           publicKey,
		CodeChallenge:       challenge,
		CodeChallengeMethod: "S256",
		Scopes:              []string{"profile"},
		Audience:            DesktopAudience,
	})
	require.NoError(t, err)
	require.ErrorIs(t, svc.ApproveAuthorization(context.Background(), 42, authz2.UserCode, true, []string{"billing"}), ErrDesktopScopeInvalid)
	values, err = rdb.HGetAll(context.Background(), desktopAuthKeyPrefix+hashDesktopCode(authz2.DeviceCode)).Result()
	require.NoError(t, err)
	require.Equal(t, "pending", values["status"])
	require.Equal(t, "profile", values["scopes"])
}

func TestDesktopDeviceReenrollRevokedThumbprintUpsertsAndClearsRevocationMarker(t *testing.T) {
	mr := miniredis.NewMiniRedis()
	if err := mr.Start(); err != nil {
		t.Skipf("miniredis unavailable in restricted environment: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	publicKey, thumbprint := p256JWK(t)
	record := desktopAuthorizationRecord{
		ClientID:            DesktopClientID,
		DeviceName:          "Re-enrolled Mac",
		PublicKeyThumbprint: thumbprint,
		Scopes:              []string{"profile"},
		Audience:            DesktopAudience,
		ProtectionLevel:     "software",
	}
	require.NoError(t, rdb.Set(context.Background(), desktopRevokedKeyPrefix+thumbprint, "1", time.Hour).Err())
	mock.ExpectExec("INSERT INTO desktop_devices").WillReturnResult(sqlmock.NewResult(1, 1))
	svc := NewDesktopDeviceServiceWithStore(db, rdb, nil, nil)
	require.NoError(t, svc.insertDevice(context.Background(), thumbprint, 42, record, "family-new", publicKey, "nonce-new"))
	_, err = rdb.Get(context.Background(), desktopRevokedKeyPrefix+thumbprint).Result()
	require.ErrorIs(t, err, redis.Nil)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDesktopDeviceReenrollDoesNotRollbackWhenRevocationCacheCleanupFails(t *testing.T) {
	// The SQL upsert is the durable enrollment commit.  Redis may be down while
	// an old device-wide/family marker is being removed; that cache failure must
	// not make ExchangeAuthorization revoke the newly issued family and strand
	// the device row in an active-but-unusable state.
	mr := miniredis.NewMiniRedis()
	if err := mr.Start(); err != nil {
		t.Skipf("miniredis unavailable in restricted environment: %v", err)
	}
	addr := mr.Addr()
	mr.Close()
	rdb := redis.NewClient(&redis.Options{
		Addr:         addr,
		MaxRetries:   0,
		DialTimeout:  10 * time.Millisecond,
		ReadTimeout:  10 * time.Millisecond,
		WriteTimeout: 10 * time.Millisecond,
	})
	t.Cleanup(func() { _ = rdb.Close() })
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	publicKey, thumbprint := p256JWK(t)
	record := desktopAuthorizationRecord{
		ClientID:            DesktopClientID,
		DeviceName:          "Re-enrolled while cache is down",
		PublicKeyThumbprint: thumbprint,
		Scopes:              []string{"profile"},
		Audience:            DesktopAudience,
		ProtectionLevel:     "software",
	}
	mock.ExpectExec("INSERT INTO desktop_devices").WillReturnResult(sqlmock.NewResult(1, 1))
	svc := NewDesktopDeviceServiceWithStore(db, rdb, nil, nil)
	require.NoError(t, svc.insertDevice(context.Background(), thumbprint, 42, record, "family-new", publicKey, "nonce-new"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIsDeviceSessionRevokedFailsClosedWhenRedisUnavailable(t *testing.T) {
	mr := miniredis.NewMiniRedis()
	if err := mr.Start(); err != nil {
		t.Skipf("miniredis unavailable in restricted environment: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: 0})
	// Close the server before the lookup. There is no database fallback in this
	// fixture, so a healthy implementation must report both an error and the
	// conservative revoked=true result rather than treating the device as live.
	mr.Close()
	t.Cleanup(func() { _ = rdb.Close() })
	revoked, err := NewDesktopDeviceServiceWithStore(nil, rdb, nil, nil).IsDeviceSessionRevoked(context.Background(), "device-1")
	require.True(t, revoked)
	require.Error(t, err)
}

func TestDesktopLogoutRequiresFreshDPoPProof(t *testing.T) {
	const refreshToken = "rt_logout-test"
	cache := &desktopRefreshCacheStub{values: map[string]*RefreshTokenData{
		hashToken(refreshToken): {
			UserID:   42,
			FamilyID: "family-logout",
			DeviceID: "device-logout",
		},
	}}
	auth := &AuthService{refreshTokenCache: cache}
	svc := NewDesktopDeviceServiceWithStore(nil, nil, auth, nil)

	// A copied refresh token alone must not be sufficient to revoke a live
	// sender-constrained device session.
	require.ErrorIs(t, svc.Logout(context.Background(), refreshToken), ErrDesktopLogoutProofInvalid)
}

func TestDesktopLogoutFailsClosedAsUnavailableWhenRefreshCacheIsDown(t *testing.T) {
	cacheErr := errors.New("redis unavailable")
	cache := &desktopRefreshCacheErrorStub{err: cacheErr}
	auth := &AuthService{refreshTokenCache: cache}
	svc := NewDesktopDeviceServiceWithStore(nil, nil, auth, nil)
	require.ErrorIs(t, svc.Logout(context.Background(), "rt_cache-down"), ErrServiceUnavailable)
}

func TestDesktopRefreshRejectsRefreshFamilyFromPreviousEnrollment(t *testing.T) {
	const refreshToken = "rt_previous-family"
	cache := &desktopRefreshCacheStub{values: map[string]*RefreshTokenData{
		hashToken(refreshToken): {
			UserID: 42, FamilyID: "family-old", DeviceID: "device-reenrolled", Audience: DesktopAudience,
		},
	}}
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	publicKey, thumbprint := p256JWK(t)
	mock.ExpectQuery("SELECT public_key_thumbprint, session_id FROM desktop_devices").
		WithArgs("device-reenrolled").
		WillReturnRows(sqlmock.NewRows([]string{"public_key_thumbprint", "session_id"}).AddRow(thumbprint, "family-new"))
	auth := &AuthService{refreshTokenCache: cache}
	svc := NewDesktopDeviceServiceWithStore(db, nil, auth, nil)
	_, err = svc.Refresh(context.Background(), DesktopDeviceRefreshInput{
		RefreshToken: refreshToken, ClientID: DesktopClientID, Audience: DesktopAudience, PublicKey: publicKey,
	})
	require.ErrorIs(t, err, ErrRefreshTokenInvalid)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRevokeDeviceForActorRejectsAnotherDesktopDevice(t *testing.T) {
	svc := NewDesktopDeviceServiceWithStore(nil, nil, nil, nil)
	err := svc.RevokeDeviceForActor(context.Background(), 42, "device-other", "device-current")
	require.ErrorIs(t, err, ErrDesktopDeviceSelfOnly)
}

func TestLegacyBrowserRefreshAndLogoutRejectDesktopRefreshToken(t *testing.T) {
	const refreshToken = "rt_desktop-legacy-route"
	cache := &desktopRefreshCacheStub{values: map[string]*RefreshTokenData{
		hashToken(refreshToken): {UserID: 42, FamilyID: "family-legacy", DeviceID: "device-1"},
	}}
	auth := &AuthService{refreshTokenCache: cache}
	_, err := auth.RefreshTokenPairForBrowser(context.Background(), refreshToken)
	require.ErrorIs(t, err, ErrDesktopRefreshRequiresDPoP)
	require.ErrorIs(t, auth.RevokeRefreshTokenForBrowser(context.Background(), refreshToken), ErrDesktopRefreshRequiresDPoP)
	_, stillPresent := cache.values[hashToken(refreshToken)]
	require.True(t, stillPresent, "legacy endpoints must not consume or delete desktop refresh tokens")
}

func TestRevokeDeviceKeepsDatabaseRevocationWhenFamilyCacheFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("UPDATE desktop_devices SET revoked_at").
		WithArgs("device-revoke", int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"session_id"}).AddRow("family-revoke"))
	cache := &desktopRefreshCacheFamilyErrorStub{err: errors.New("redis unavailable")}
	auth := &AuthService{refreshTokenCache: cache}
	svc := NewDesktopDeviceServiceWithStore(db, nil, auth, nil)

	// The SQL row is revoked before the cache operation. If Redis is down the
	// method reports an outage, but JWT middleware can still fail closed via the
	// persisted revoked_at column rather than treating the device as active.
	require.ErrorIs(t, svc.RevokeDevice(context.Background(), 42, "device-revoke"), ErrServiceUnavailable)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDesktopDeviceReenrollRejectsActiveOrCrossUserThumbprint(t *testing.T) {
	tests := []struct {
		name       string
		existingID int64
		revoked    any
		want       error
	}{
		{name: "same user active", existingID: 42, revoked: nil, want: ErrDesktopDeviceAlreadyActive},
		{name: "other user revoked", existingID: 99, revoked: time.Now().UTC(), want: ErrDesktopDeviceKeyOwned},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			publicKey, thumbprint := p256JWK(t)
			record := desktopAuthorizationRecord{ClientID: DesktopClientID, DeviceName: "Existing", PublicKeyThumbprint: thumbprint, Scopes: []string{"profile"}, Audience: DesktopAudience}
			mock.ExpectExec("INSERT INTO desktop_devices").WillReturnResult(sqlmock.NewResult(0, 0))
			rows := sqlmock.NewRows([]string{"user_id", "revoked_at"}).AddRow(tc.existingID, tc.revoked)
			mock.ExpectQuery("SELECT user_id, revoked_at").WithArgs(thumbprint).WillReturnRows(rows)
			svc := NewDesktopDeviceServiceWithStore(db, nil, nil, nil)
			err = svc.insertDevice(context.Background(), thumbprint, 42, record, "family-new", publicKey, "nonce-new")
			require.ErrorIs(t, err, tc.want)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestIsDeviceSessionRevokedFailsClosedWhenEnrollmentCannotBeChecked(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("SELECT revoked_at FROM desktop_devices").WithArgs("missing-device").WillReturnError(sql.ErrNoRows)

	svc := NewDesktopDeviceServiceWithStore(db, nil, nil, nil)
	revoked, err := svc.IsDeviceSessionRevoked(context.Background(), "missing-device")
	require.NoError(t, err)
	require.True(t, revoked, "a deleted enrollment must invalidate its access token")
	require.NoError(t, mock.ExpectationsWereMet())

	withoutDB := NewDesktopDeviceServiceWithStore(nil, nil, nil, nil)
	revoked, err = withoutDB.IsDeviceSessionRevoked(context.Background(), "device-without-db")
	require.ErrorIs(t, err, ErrServiceUnavailable)
	require.True(t, revoked, "an unavailable enrollment store must fail closed")
}

func TestIsDeviceSessionRevokedForSessionRejectsOldFamilyAfterReenroll(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("SELECT session_id, revoked_at FROM desktop_devices").WithArgs("device-1").
		WillReturnRows(sqlmock.NewRows([]string{"session_id", "revoked_at"}).AddRow("family-new", nil))
	svc := NewDesktopDeviceServiceWithStore(db, nil, nil, nil)
	revoked, err := svc.IsDeviceSessionRevokedForSession(context.Background(), "device-1", "family-old")
	require.NoError(t, err)
	require.True(t, revoked)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIsDeviceSessionRevokedForSessionAllowsCurrentFamily(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("SELECT session_id, revoked_at FROM desktop_devices").WithArgs("device-1").
		WillReturnRows(sqlmock.NewRows([]string{"session_id", "revoked_at"}).AddRow("family-current", nil))
	svc := NewDesktopDeviceServiceWithStore(db, nil, nil, nil)
	revoked, err := svc.IsDeviceSessionRevokedForSession(context.Background(), "device-1", "family-current")
	require.NoError(t, err)
	require.False(t, revoked)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIsDeviceSessionRevokedForSessionIgnoresStaleFamilyMarkerAfterReenroll(t *testing.T) {
	mr := miniredis.NewMiniRedis()
	if err := mr.Start(); err != nil {
		t.Skipf("miniredis unavailable in restricted environment: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	const deviceID = "device-reenrolled"
	const oldFamily = "family-old"
	const currentFamily = "family-current"
	require.NoError(t, rdb.Set(context.Background(), desktopRevokedKeyPrefix+deviceID, oldFamily, time.Hour).Err())
	mock.ExpectQuery("SELECT session_id, revoked_at FROM desktop_devices").WithArgs(deviceID).
		WillReturnRows(sqlmock.NewRows([]string{"session_id", "revoked_at"}).AddRow(currentFamily, nil))

	svc := NewDesktopDeviceServiceWithStore(db, rdb, nil, nil)
	revoked, err := svc.IsDeviceSessionRevokedForSession(context.Background(), deviceID, currentFamily)
	require.NoError(t, err)
	require.False(t, revoked, "a marker for the previous family must not revoke the re-enrolled session")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestIsDeviceSessionRevokedForSessionMatchesFamilyMarker(t *testing.T) {
	mr := miniredis.NewMiniRedis()
	if err := mr.Start(); err != nil {
		t.Skipf("miniredis unavailable in restricted environment: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	const deviceID = "device-current"
	const familyID = "family-current"
	require.NoError(t, rdb.Set(context.Background(), desktopRevokedKeyPrefix+deviceID, familyID, time.Hour).Err())
	svc := NewDesktopDeviceServiceWithStore(nil, rdb, nil, nil)
	revoked, err := svc.IsDeviceSessionRevokedForSession(context.Background(), deviceID, familyID)
	require.NoError(t, err)
	require.True(t, revoked)
}

func TestVerifyDPoPSignatureRequiresJOSEFixedWidthEncoding(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	input := []byte("dpop signing input")
	digest := sha256.Sum256(input)
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	require.NoError(t, err)

	// ES256 in compact JWS/DPoP is the fixed-width raw R || S form.
	jose := make([]byte, 64)
	r.FillBytes(jose[:32])
	s.FillBytes(jose[32:])
	require.True(t, verifyDPoPSignature(&key.PublicKey, input, jose))

	// ASN.1 DER is a different signature serialization and must not be accepted
	// by this endpoint, even when it verifies cryptographically.
	der, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	require.NoError(t, err)
	require.NotEqual(t, 64, len(der))
	require.False(t, verifyDPoPSignature(&key.PublicKey, input, der))

	// A malformed 64-byte value must still fail cryptographic verification.
	zero := make([]byte, 64)
	require.False(t, verifyDPoPSignature(&key.PublicKey, input, zero))
}
