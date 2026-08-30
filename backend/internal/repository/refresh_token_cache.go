package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	refreshTokenKeyPrefix                   = "refresh_token:"
	refreshTokenUsedKeyPrefix               = "refresh_token_used:"
	userRefreshTokensPrefix                 = "user_refresh_tokens:"
	tokenFamilyPrefix                       = "token_family:"
	refreshTokenFamilyRevokedMember         = "__sub2api_family_revoked__"
	refreshTokenFamilyRevocationFallbackTTL = 24 * time.Hour
)

// refreshTokenKey generates the Redis key for a refresh token.
func refreshTokenKey(tokenHash string) string {
	return refreshTokenKeyPrefix + tokenHash
}

func refreshTokenUsedKey(tokenHash string) string {
	return refreshTokenUsedKeyPrefix + tokenHash
}

// userRefreshTokensKey generates the Redis key for user's token set.
func userRefreshTokensKey(userID int64) string {
	return fmt.Sprintf("%s%d", userRefreshTokensPrefix, userID)
}

// tokenFamilyKey generates the Redis key for token family set.
func tokenFamilyKey(familyID string) string {
	return tokenFamilyPrefix + familyID
}

type refreshTokenCache struct {
	rdb *redis.Client
}

// NewRefreshTokenCache creates a new RefreshTokenCache implementation.
func NewRefreshTokenCache(rdb *redis.Client) service.RefreshTokenCache {
	return &refreshTokenCache{rdb: rdb}
}

func (c *refreshTokenCache) StoreRefreshToken(ctx context.Context, tokenHash string, data *service.RefreshTokenData, ttl time.Duration) error {
	key := refreshTokenKey(tokenHash)
	val, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal refresh token data: %w", err)
	}
	if c == nil || c.rdb == nil || data == nil || ttl <= 0 {
		return fmt.Errorf("invalid refresh token cache state")
	}
	// Store the token and register it in the family set in one Redis operation.
	// The family-revoked sentinel is checked before either write, so an
	// in-flight refresh cannot mint a token after another request detected reuse.
	result, err := storeRefreshTokenIfFamilyActiveScript.Run(
		ctx,
		c.rdb,
		[]string{key, tokenFamilyKey(data.FamilyID)},
		tokenHash,
		val,
		ttl.Milliseconds(),
		refreshTokenFamilyRevokedMember,
	).Int()
	if err != nil {
		return err
	}
	if result != 1 {
		return service.ErrRefreshTokenFamilyRevoked
	}
	return nil
}

func (c *refreshTokenCache) GetRefreshToken(ctx context.Context, tokenHash string) (*service.RefreshTokenData, error) {
	key := refreshTokenKey(tokenHash)
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, service.ErrRefreshTokenNotFound
		}
		return nil, err
	}
	var data service.RefreshTokenData
	if err := json.Unmarshal([]byte(val), &data); err != nil {
		return nil, fmt.Errorf("unmarshal refresh token data: %w", err)
	}
	return &data, nil
}

// ConsumeRefreshToken atomically rotates a refresh token. The active value is
// deleted and copied to a short-lived used-token tombstone in the same Lua
// execution. A later caller receives the tombstone (reused=true), allowing the
// service layer to revoke the whole family even though the original token is
// no longer active. No raw token is ever persisted; both keys are addressed by
// its hash.
func (c *refreshTokenCache) ConsumeRefreshToken(ctx context.Context, tokenHash string) (*service.RefreshTokenData, bool, error) {
	if c == nil || c.rdb == nil || tokenHash == "" {
		return nil, false, service.ErrRefreshTokenNotFound
	}
	value, err := consumeRefreshTokenScript.Run(
		ctx,
		c.rdb,
		[]string{refreshTokenKey(tokenHash), refreshTokenUsedKey(tokenHash)},
	).Text()
	if err != nil {
		return nil, false, err
	}
	if value == "" {
		return nil, false, service.ErrRefreshTokenNotFound
	}
	if len(value) < 2 || (value[0] != 'C' && value[0] != 'R') || value[1] != ':' {
		return nil, false, fmt.Errorf("invalid refresh token consume result")
	}
	var data service.RefreshTokenData
	if err := json.Unmarshal([]byte(value[2:]), &data); err != nil {
		return nil, false, fmt.Errorf("unmarshal consumed refresh token data: %w", err)
	}
	return &data, value[0] == 'R', nil
}

func (c *refreshTokenCache) DeleteRefreshToken(ctx context.Context, tokenHash string) error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.Del(ctx, refreshTokenKey(tokenHash), refreshTokenUsedKey(tokenHash)).Err()
}

func (c *refreshTokenCache) DeleteUserRefreshTokens(ctx context.Context, userID int64) error {
	// Get all token hashes for this user
	tokenHashes, err := c.GetUserTokenHashes(ctx, userID)
	if err != nil && err != redis.Nil {
		return fmt.Errorf("get user token hashes: %w", err)
	}

	if len(tokenHashes) == 0 {
		return nil
	}

	// Build keys to delete
	keys := make([]string, 0, len(tokenHashes)+1)
	for _, hash := range tokenHashes {
		keys = append(keys, refreshTokenKey(hash))
		keys = append(keys, refreshTokenUsedKey(hash))
	}
	keys = append(keys, userRefreshTokensKey(userID))

	// Delete all keys in a pipeline
	pipe := c.rdb.Pipeline()
	for _, key := range keys {
		pipe.Del(ctx, key)
	}
	_, err = pipe.Exec(ctx)
	return err
}

func (c *refreshTokenCache) DeleteTokenFamily(ctx context.Context, familyID string) error {
	if c == nil || c.rdb == nil || familyID == "" {
		return nil
	}
	// Mark the family revoked and delete every active/consumed token in one
	// Redis execution. Keeping the sentinel in the family set closes the race
	// where a concurrent refresh stores a newly rotated token after the family
	// was enumerated for deletion.
	_, err := deleteTokenFamilyScript.Run(
		ctx,
		c.rdb,
		[]string{tokenFamilyKey(familyID)},
		refreshTokenKeyPrefix,
		refreshTokenUsedKeyPrefix,
		refreshTokenFamilyRevokedMember,
		refreshTokenFamilyRevocationFallbackTTL.Milliseconds(),
	).Result()
	return err
}

func (c *refreshTokenCache) AddToUserTokenSet(ctx context.Context, userID int64, tokenHash string, ttl time.Duration) error {
	key := userRefreshTokensKey(userID)
	pipe := c.rdb.Pipeline()
	pipe.SAdd(ctx, key, tokenHash)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *refreshTokenCache) AddToFamilyTokenSet(ctx context.Context, familyID string, tokenHash string, ttl time.Duration) error {
	if c == nil || c.rdb == nil || familyID == "" || tokenHash == "" || ttl <= 0 {
		return fmt.Errorf("invalid refresh token family state")
	}
	result, err := addToFamilyTokenSetIfActiveScript.Run(
		ctx,
		c.rdb,
		[]string{tokenFamilyKey(familyID)},
		tokenHash,
		ttl.Milliseconds(),
		refreshTokenFamilyRevokedMember,
	).Int()
	if err != nil {
		return err
	}
	if result != 1 {
		return service.ErrRefreshTokenFamilyRevoked
	}
	return nil
}

func (c *refreshTokenCache) GetUserTokenHashes(ctx context.Context, userID int64) ([]string, error) {
	key := userRefreshTokensKey(userID)
	return c.rdb.SMembers(ctx, key).Result()
}

func (c *refreshTokenCache) GetFamilyTokenHashes(ctx context.Context, familyID string) ([]string, error) {
	key := tokenFamilyKey(familyID)
	hashes, err := c.rdb.SMembers(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	filtered := hashes[:0]
	for _, hash := range hashes {
		if hash != refreshTokenFamilyRevokedMember {
			filtered = append(filtered, hash)
		}
	}
	return filtered, nil
}

func (c *refreshTokenCache) IsTokenInFamily(ctx context.Context, familyID string, tokenHash string) (bool, error) {
	key := tokenFamilyKey(familyID)
	return c.rdb.SIsMember(ctx, key, tokenHash).Result()
}

// storeRefreshTokenIfFamilyActiveScript atomically checks the family revoke
// sentinel, stores the token with its expiry, and registers its hash in the
// family set. The latter registration is important: a family revoke racing a
// token rotation will either see and delete this hash or cause this write to
// fail before any token is minted.
var storeRefreshTokenIfFamilyActiveScript = redis.NewScript(`
if redis.call('SISMEMBER', KEYS[2], ARGV[4]) == 1 then
    return 0
end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
redis.call('SADD', KEYS[2], ARGV[1])
redis.call('PEXPIRE', KEYS[2], ARGV[3])
return 1
`)

// consumeRefreshTokenScript returns C:<json> for the first consumer and
// R:<json> for a replay. The replay tombstone has the original token's TTL,
// retaining family metadata only for the window in which reuse can matter.
var consumeRefreshTokenScript = redis.NewScript(`
local active = redis.call('GET', KEYS[1])
if active then
    local ttl = redis.call('PTTL', KEYS[1])
    redis.call('DEL', KEYS[1])
    if ttl > 0 then
        redis.call('SET', KEYS[2], active, 'PX', ttl)
    end
    return 'C:' .. active
end
local used = redis.call('GET', KEYS[2])
if used then
    return 'R:' .. used
end
return ''
`)

// deleteTokenFamilyScript marks a family revoked before enumerating it. The
// marker remains in the family set with the previous set TTL (or a bounded
// fallback), so StoreRefreshToken/AddToFamilyTokenSet cannot recreate tokens
// after a reuse race. It also removes consumed-token tombstones.
var deleteTokenFamilyScript = redis.NewScript(`
local family_key = KEYS[1]
local ttl = redis.call('PTTL', family_key)
if ttl <= 0 then
    ttl = tonumber(ARGV[4])
end
redis.call('SADD', family_key, ARGV[3])
redis.call('PEXPIRE', family_key, ttl)
local hashes = redis.call('SMEMBERS', family_key)
for _, hash in ipairs(hashes) do
    if hash ~= ARGV[3] then
        redis.call('DEL', ARGV[1] .. hash)
        redis.call('DEL', ARGV[2] .. hash)
        redis.call('SREM', family_key, hash)
    end
end
return #hashes
`)

// addToFamilyTokenSetIfActiveScript keeps legacy callers that register a
// token separately safe: a revoked family rejects the registration atomically.
var addToFamilyTokenSetIfActiveScript = redis.NewScript(`
if redis.call('SISMEMBER', KEYS[1], ARGV[3]) == 1 then
    return 0
end
redis.call('SADD', KEYS[1], ARGV[1])
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`)
