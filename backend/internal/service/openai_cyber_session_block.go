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
	SetCyberSessionBlocked(ctx context.Context, scopeKey string, keys []string, ttl time.Duration) error
	IsCyberSessionScopeActive(ctx context.Context, scopeKey string) (bool, error)
	FindCyberSessionBlocked(ctx context.Context, keys []string) (string, error)
}

// legacyCyberSessionBlockStore keeps compatibility with the original fork
// cache contract used by older deployments and test doubles.
type legacyCyberSessionBlockStore interface {
	SetCyberSessionBlocked(context.Context, string, time.Duration) error
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
	if raw == "" {
		return ""
	}
	isolated := isolateOpenAISessionID(apiKeyID, raw)
	sum := sha256.Sum256([]byte(isolated))
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
// 在异步审计 context 中做一次幂等补写。
func (s *OpenAIGatewayService) MarkCyberSessionBlocked(ctx context.Context, key string, keySets ...[]string) bool {
	keys := []string{key}
	scopeKey := ""
	if len(keySets) > 0 {
		scopeKey = key
		keys = keySets[0]
	}
	if len(keys) == 0 {
		return false
	}
	enabled, ttl := s.CyberSessionBlockRuntime(ctx)
	if !enabled {
		return false
	}
	store := s.cyberSessionBlockStore()
	if store != nil {
		if err := store.SetCyberSessionBlocked(ctx, scopeKey, keys, ttl); err != nil {
			logger.LegacyPrintf("service.openai_gateway", "cyber session block write failed: err=%v", err)
			return false
		}
		return true
	}
	legacy, ok := s.cache.(legacyCyberSessionBlockStore)
	if !ok {
		return false
	}
	if err := legacy.SetCyberSessionBlocked(ctx, firstNonEmptyString(keys), ttl); err != nil {
		logger.LegacyPrintf("service.openai_gateway", "cyber session block write failed: err=%v", err)
		return false
	}
	return true
}

const cyberSessionTranscriptLookupOverflowBlockKey = "transcript_lookup_limit_exceeded"

func CyberSessionExplicitBlockKey(apiKeyID int64, c *gin.Context, body []byte) string {
	return hashCyberSessionBlockKey(apiKeyID, explicitOpenAISessionID(c, body))
}

func CyberSessionScopeKey(apiKeyID int64, clientIP, userAgent string) string {
	if apiKeyID <= 0 {
		return ""
	}
	raw := "cyber-scope:v1|api_key=" + strconv.FormatInt(apiKeyID, 10) + "|ip=" + strings.TrimSpace(clientIP) + "|ua=" + NormalizeSessionUserAgent(userAgent)
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
	scopeKey := CyberSessionScopeKey(apiKeyID, clientIP, userAgent)
	active, err := store.IsCyberSessionScopeActive(ctx, scopeKey)
	if err != nil {
		logger.LegacyPrintf("service.openai_gateway", "cyber session scope read failed: err=%v", err)
		return ""
	}
	if !active {
		return ""
	}
	transcript := deriveOpenAICyberTranscriptBlockKeys(apiKeyID, body)
	if transcript.lookupKeysTruncated {
		// Once the coarse scope is active, silently dropping old candidates would
		// let a blocked client evade prefix matching by appending dummy items.
		return cyberSessionTranscriptLookupOverflowBlockKey
	}
	keys := transcript.lookupKeys
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
