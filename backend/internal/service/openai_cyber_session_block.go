package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

// CyberSessionBlockStore 是 cyber 会话屏蔽表的存取接口。
// repository 层 gatewayCache 附带实现（类型断言探测接入，不改 GatewayCache
// 共享接口）；测试 stub 不实现时屏蔽能力自动降级关闭。
type CyberSessionBlockStore interface {
	SetCyberSessionBlocked(ctx context.Context, block CyberSessionBlock, ttl time.Duration) error
	// CyberSessionTranscriptLengths returns the recorded transcript block
	// lengths of the API key, or none when the source scope is inactive.
	CyberSessionTranscriptLengths(ctx context.Context, apiKeyID int64, scopeKey string) ([]int, error)
	FindCyberSessionBlocked(ctx context.Context, keys []string) (string, error)
}

// legacyCyberSessionBlockStore keeps compatibility with the original fork
// cache contract used by older deployments and test doubles.
type legacyCyberSessionBlockStore interface {
	SetCyberSessionBlocked(context.Context, string, time.Duration) error
}

// CyberSessionBlock is one cyber hit to persist. Keys are exact block keys;
// TranscriptLengths records the history length of each transcript key among
// them and, like ScopeKey, is only meaningful for transcript matching.
type CyberSessionBlock struct {
	APIKeyID          int64
	ScopeKey          string
	Keys              []string
	TranscriptLengths []int
}

// CyberSessionBlockKey 派生会话屏蔽 key：仅用显式会话标识（header
// session_id/conversation_id 或 body prompt_cache_key），混入 apiKeyID 隔离后
// sha256。无显式标识返回空串——调用方必须放行（粒度决策：不退化到
// user/apikey/内容派生）。
func CyberSessionBlockKey(apiKeyID int64, c *gin.Context, body []byte) string {
	return CyberSessionBlockKeyWithFallback(apiKeyID, c, body, "")
}

// CyberSessionBlockKeyWithFallback 保持显式 header/body 会话标识优先，仅当它们
// 均为空时使用调用方提供的端点专用显式标识。当前供 Alpha Search 的 body.id
// 使用；其他 Responses 路径继续调用 CyberSessionBlockKey，不会退化到内容派生。
func CyberSessionBlockKeyWithFallback(apiKeyID int64, c *gin.Context, body []byte, fallback string) string {
	raw := explicitOpenAISessionID(c, body)
	if raw == "" {
		raw = strings.TrimSpace(fallback)
	}
	return hashCyberSessionBlockKey(apiKeyID, raw)
}

// CyberSessionExplicitBlockKey returns an inexpensive exact key when the
// client supplies a stable session signal.
func CyberSessionExplicitBlockKey(apiKeyID int64, c *gin.Context, body []byte) string {
	return hashCyberSessionBlockKey(apiKeyID, explicitOpenAISessionID(c, body))
}

// CyberSessionScopeKey is a coarse, non-blocking fingerprint used only to
// avoid transcript parsing and MGET for sources that never produced a hit.
func CyberSessionScopeKey(apiKeyID int64, clientIP, userAgent string) string {
	if apiKeyID <= 0 {
		return ""
	}
	raw := "cyber-scope:v1|api_key=" + strconv.FormatInt(apiKeyID, 10) +
		"|ip=" + strings.TrimSpace(clientIP) +
		"|ua=" + NormalizeSessionUserAgent(userAgent)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func hashCyberSessionBlockKey(apiKeyID int64, raw string) string {
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(isolateOpenAISessionID(apiKeyID, raw)))
	return hex.EncodeToString(sum[:])
}

// cyberSessionBlockStore 探测 cache 是否具备屏蔽存储能力。
// 注意：若未来以装饰器包装 GatewayCache（如日志/指标装饰器），该装饰器必须同时实现
// CyberSessionBlockStore，否则会话屏蔽能力将静默降级关闭
// （编译断言 var _ service.CyberSessionBlockStore = (*gatewayCache)(nil) 只覆盖
// *gatewayCache 本体，无法覆盖其外层包装）。
func (s *OpenAIGatewayService) cyberSessionBlockStore() CyberSessionBlockStore {
	if s == nil || s.cache == nil {
		return nil
	}
	store, ok := s.cache.(CyberSessionBlockStore)
	if !ok {
		return nil
	}
	return store
}

// CyberSessionBlockRuntime 返回 (开关, TTL)。开关默认关。
// 委托给 SettingService.GetCyberSessionBlockRuntime，进程内缓存避免热路径 DB 往返。
func (s *OpenAIGatewayService) CyberSessionBlockRuntime(ctx context.Context) (bool, time.Duration) {
	if s == nil || s.settingService == nil {
		return false, time.Hour
	}
	return s.settingService.GetCyberSessionBlockRuntime(ctx)
}

// MarkCyberSessionBlocked 把会话写入屏蔽表（写入点：cyber 命中后），返回是否
// 已成功写入。开关关闭、key 为空、存储不可用或写入失败时返回 false，调用方可
// 在异步审计 context 中做一次幂等补写。仅实现旧版单 key 契约的存储只写入首个 key。
func (s *OpenAIGatewayService) MarkCyberSessionBlocked(ctx context.Context, block CyberSessionBlock) bool {
	if s == nil || firstCyberSessionBlockKey(block.Keys) == "" {
		return false
	}
	enabled, ttl := s.CyberSessionBlockRuntime(ctx)
	if !enabled {
		return false
	}
	if store := s.cyberSessionBlockStore(); store != nil {
		if err := store.SetCyberSessionBlocked(ctx, block, ttl); err != nil {
			logger.LegacyPrintf("service.openai_gateway", "cyber session block write failed: err=%v", err)
			return false
		}
		return true
	}
	legacy, ok := s.cache.(legacyCyberSessionBlockStore)
	if !ok {
		return false
	}
	if err := legacy.SetCyberSessionBlocked(ctx, firstCyberSessionBlockKey(block.Keys), ttl); err != nil {
		logger.LegacyPrintf("service.openai_gateway", "cyber session block write failed: err=%v", err)
		return false
	}
	return true
}

// MarkCyberSessionBlockedKey 写入单个显式会话 key（如 Alpha Search 的 body.id）。
func (s *OpenAIGatewayService) MarkCyberSessionBlockedKey(ctx context.Context, key string) bool {
	return s.MarkCyberSessionBlocked(ctx, CyberSessionBlock{Keys: []string{key}})
}

// FindCyberSessionBlockedForRequest applies explicit-first lookup followed by
// scope-gated transcript matching. All failures remain fail-open.
func (s *OpenAIGatewayService) FindCyberSessionBlockedForRequest(ctx context.Context, apiKeyID int64, c *gin.Context, body []byte, clientIP, userAgent string) string {
	enabled, _ := s.CyberSessionBlockRuntime(ctx)
	if !enabled {
		return ""
	}
	store := s.cyberSessionBlockStore()
	if store == nil {
		return ""
	}
	if explicitKey := CyberSessionExplicitBlockKey(apiKeyID, c, body); explicitKey != "" {
		key, err := store.FindCyberSessionBlocked(ctx, []string{explicitKey})
		if err != nil {
			logger.LegacyPrintf("service.openai_gateway", "cyber explicit session read failed: err=%v", err)
			return ""
		}
		if key != "" {
			return key
		}
	}
	lengths, err := store.CyberSessionTranscriptLengths(ctx, apiKeyID, CyberSessionScopeKey(apiKeyID, clientIP, userAgent))
	if err != nil {
		logger.LegacyPrintf("service.openai_gateway", "cyber session scope read failed: err=%v", err)
		return ""
	}
	// Blocked prefixes are recomputed only at their recorded lengths, so any
	// number of appended items still reaches them and history length alone
	// never decides a block.
	keys := cyberSessionTranscriptKeysAt(apiKeyID, body, lengths)
	if len(keys) == 0 {
		return ""
	}
	key, err := store.FindCyberSessionBlocked(ctx, keys)
	if err != nil {
		logger.LegacyPrintf("service.openai_gateway", "cyber session block batch read failed: err=%v", err)
		return ""
	}
	return key
}

func (s *OpenAIGatewayService) FindCyberSessionBlockedKey(ctx context.Context, key string) string {
	if s == nil || strings.TrimSpace(key) == "" {
		return ""
	}
	enabled, _ := s.CyberSessionBlockRuntime(ctx)
	if !enabled {
		return ""
	}
	store := s.cyberSessionBlockStore()
	if store == nil {
		return ""
	}
	found, err := store.FindCyberSessionBlocked(ctx, []string{key})
	if err != nil {
		return ""
	}
	return found
}

func firstCyberSessionBlockKey(keys []string) string {
	for _, key := range keys {
		if key = strings.TrimSpace(key); key != "" {
			return key
		}
	}
	return ""
}
