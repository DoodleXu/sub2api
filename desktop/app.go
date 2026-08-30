package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/desktop/internal/configwriter"
	"github.com/Wei-Shaw/sub2api/desktop/internal/imagestore"
	"github.com/Wei-Shaw/sub2api/desktop/internal/securestore"
	"github.com/Wei-Shaw/sub2api/desktop/internal/siteclient"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	appName    = "神奇AI助手"
	appVersion = "0.1.0"
	// Local cleanup must still run when the request that discovered a failed
	// transition has already been canceled (for example, when the Wails window
	// is closed while a keyring write is in flight). Keep the detached window
	// short so a broken credential manager cannot hold shutdown indefinitely.
	localCleanupTimeout = 5 * time.Second
	// Keep the legacy image ref stable for existing installations. Purpose-
	// specific refs prevent a Claude/Codex integration choice from silently
	// replacing the key used by the image gateway.
	apiKeyRef       = "sub2api/default-api-key"
	codexAPIKeyRef  = "sub2api/codex-api-key"
	claudeAPIKeyRef = "sub2api/claude-api-key"
	// accessTokenRef is retained only to remove credentials written by early
	// development builds. New builds keep access tokens in process memory.
	accessTokenRef  = "sub2api/default-access-token"
	refreshTokenRef = "sub2api/default-refresh-token"
	dpopKeyRef      = "sub2api/default-dpop-key"
	defaultModel    = "gpt-image-2"
	defaultImages   = 32 << 20
)

// apiKeySecretRefs is deliberately a closed set.  Connection metadata is
// user-editable JSON, so cleanup and account transitions must never use a
// renderer/config supplied keyring name as a lookup or deletion target.
var apiKeySecretRefs = [...]string{apiKeyRef, codexAPIKeyRef, claudeAPIKeyRef}

// App is the Wails binding surface. Methods return DTOs with no secret fields;
// the API key is held by securestore and only passed to siteclient internally.
type App struct {
	mu sync.RWMutex
	// accountMu serializes identity/key transitions with account-bound work.
	// Read locks cover requests that load a key or device token; write locks
	// cover account replacement, device authorization, logout and revocation.
	// This prevents a background recovery request from retaining the previous
	// account's secret while the configuration is being switched.
	accountMu sync.RWMutex
	ctx       context.Context
	config    configwriter.Writer
	secrets   securestore.Store
	images    imagestore.Store
	tasks     imagestore.TaskStore
	client    *siteclient.HTTPClient
	// Access tokens are intentionally process-memory only. Refresh tokens and
	// the DPoP private key live in the OS credential store; a restart obtains a
	// fresh short-lived access token through the rotated refresh flow.
	sessionAccessToken     string
	sessionAccessExpiresAt time.Time
	refreshMu              sync.Mutex
	recoveryMu             sync.Mutex
	recoveryRunning        bool
	imageHandleMu          sync.Mutex
	imageHandles           map[string]imageFileHandle
	// logoutDeviceSession is kept as a narrow injection point for tests. The
	// production value is nil and uses HTTPClient.LogoutDevice after restoring
	// the sender-constrained proof from the OS credential store.
	logoutDeviceSession func(context.Context, *siteclient.HTTPClient, string) error
	lastChecked         time.Time
	pending             map[string]*pendingDeviceAuthorization
}

type pendingDeviceAuthorization struct {
	client *siteclient.HTTPClient
	proof  *siteclient.DeviceProof
	auth   siteclient.DeviceAuthorization
	url    string
	until  time.Time
}

func NewApp() *App {
	root := appDataDir()
	config, err := configwriter.NewJSONWriter(filepath.Join(root, "connection.json"))
	if err != nil {
		panic(err)
	}
	images, err := imagestore.NewFileStore(filepath.Join(root, "images"), defaultImages)
	if err != nil {
		panic(err)
	}
	taskDB, err := imagestore.NewSQLiteTaskStore(filepath.Join(root, "image-tasks.sqlite3"))
	if err != nil {
		panic(err)
	}
	// Import checkpoints written by pre-SQLite development builds. The legacy
	// file is retained as a recoverable migration source and is never used for
	// new writes.
	if err := taskDB.MigrateJSON(context.Background(), filepath.Join(root, "image-tasks.json")); err != nil {
		_ = taskDB.Close()
		panic(err)
	}
	return &App{
		config:       config,
		secrets:      securestore.NewPlatformStore(),
		images:       images,
		tasks:        taskDB,
		pending:      make(map[string]*pendingDeviceAuthorization),
		imageHandles: make(map[string]imageFileHandle),
	}
}

func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	a.ctx = ctx
	a.mu.Unlock()
	// Resume all scoped, non-terminal checkpoints in the background. The
	// coordinator is deliberately independent of the currently visible route;
	// a user can leave the app on Overview while an async image task completes.
	a.startTaskRecovery()
}

// AppInfo is intentionally static and contains no environment or credential
// details. It gives the frontend a stable place to display build information.
type AppInfo struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	OfficialSiteURL string `json:"official_site_url"`
}

func (a *App) GetAppInfo() AppInfo {
	return AppInfo{Name: appName, Version: appVersion, OfficialSiteURL: siteclient.OfficialSiteURL}
}

// GetCapabilities returns the public site contract from the pinned official
// origin. It is safe to call before any local credentials exist.
func (a *App) GetCapabilities() (siteclient.ClientCapabilities, error) {
	ctx, cancel := context.WithTimeout(a.appContext(), 10*time.Second)
	defer cancel()
	client, err := siteclient.New(siteclient.OfficialSiteURL, "")
	if err != nil {
		return siteclient.ClientCapabilities{}, err
	}
	return client.Capabilities(ctx)
}

// GetIntegrationProfiles returns the ownership-checked integration contract
// for one API key.  The request is made through the proof-bound desktop
// session rather than accepting a key secret from the renderer; the server
// enforces the api_keys consent scope and never returns secret material.
func (a *App) GetIntegrationProfiles(apiKeyID int64) (siteclient.IntegrationProfileResponse, error) {
	if apiKeyID <= 0 {
		return siteclient.IntegrationProfileResponse{}, errors.New("API key ID 无效")
	}
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx, cancel := context.WithTimeout(a.appContext(), 15*time.Second)
	defer cancel()
	return runSessionRequest(a, ctx, func(client *siteclient.HTTPClient, token string) (siteclient.IntegrationProfileResponse, error) {
		return client.IntegrationProfiles(ctx, token, apiKeyID)
	})
}

// GetImageCapabilities returns the public Images API contract. If a gateway
// has already been discovered it is used; otherwise the official origin is
// queried. No credential is sent for this endpoint.
func (a *App) GetImageCapabilities() (siteclient.ImageCapabilities, error) {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx, cancel := context.WithTimeout(a.appContext(), 10*time.Second)
	defer cancel()
	siteURL, gatewayURL := siteclient.OfficialSiteURL, ""
	if config, err := a.config.Load(ctx); err == nil {
		if validateErr := validateOfficialConfig(&config); validateErr == nil {
			siteURL, gatewayURL = config.SiteURL, config.GatewayURL
		}
	}
	client, err := siteclient.New(siteURL, gatewayURL)
	if err != nil {
		return siteclient.ImageCapabilities{}, err
	}
	return client.ImageCapabilities(ctx)
}

type ConnectionInput struct {
	SiteURL    string `json:"site_url"`
	GatewayURL string `json:"gateway_url"`
	APIKey     string `json:"api_key"`
	Label      string `json:"label"`
}

type ConnectionSummary struct {
	Configured        bool   `json:"configured"`
	AuthMode          string `json:"auth_mode,omitempty"`
	SiteURL           string `json:"site_url"`
	GatewayURL        string `json:"gateway_url"`
	Label             string `json:"label"`
	APIKeyConfigured  bool   `json:"api_key_configured"`
	APIKeyHint        string `json:"api_key_hint"`
	APIKeyID          int64  `json:"api_key_id,omitempty"`
	CodexAPIKeyID     int64  `json:"codex_api_key_id,omitempty"`
	ClaudeAPIKeyID    int64  `json:"claude_api_key_id,omitempty"`
	SessionConfigured bool   `json:"session_configured"`
	DeviceID          string `json:"device_id,omitempty"`
	ProtectionLevel   string `json:"protection_level,omitempty"`
	Scope             string `json:"scope,omitempty"`
	UpdatedAt         string `json:"updated_at,omitempty"`
}

// ToolConfigInput selects one local coding client to configure. The API key is
// intentionally not part of this binding: IntegrateToolConfig loads it from
// the OS keyring after validating the pinned official connection.
type ToolConfigInput struct {
	Tool    string `json:"tool"`
	BaseURL string `json:"base_url,omitempty"`
	Model   string `json:"model,omitempty"`
}

type ToolConfigFile struct {
	Path           string `json:"path"`
	BackupPath     string `json:"backup_path,omitempty"`
	Changed        bool   `json:"changed"`
	ContainsSecret bool   `json:"contains_secret"`
}

// ToolLaunchPlan describes how the native client can start a configured CLI
// without placing its API key in a shell profile or command-line argument.
// The command asks the installed desktop binary to read the key from the OS
// credential store at launch time and injects it only into the child process.
type ToolLaunchPlan struct {
	Tool                string `json:"tool"`
	EnvironmentVariable string `json:"environment_variable"`
	Command             string `json:"command"`
	Shell               string `json:"shell"`
	Note                string `json:"note,omitempty"`
}

type ToolConfigResult struct {
	Tool        string           `json:"tool"`
	Files       []ToolConfigFile `json:"files"`
	Warnings    []string         `json:"warnings,omitempty"`
	Launch      *ToolLaunchPlan  `json:"launch,omitempty"`
	CompletedAt string           `json:"completed_at"`
}

type ToolConfigRestoreInput struct {
	Tool       string `json:"tool"`
	BackupPath string `json:"backup_path"`
}

type ToolConfigRestoreResult struct {
	Tool               string `json:"tool"`
	TargetPath         string `json:"target_path"`
	PreviousBackupPath string `json:"previous_backup_path,omitempty"`
}

// APIKeySummary is safe to return to the renderer. The full key is fetched
// only inside SelectAPIKey and is written directly to the platform keyring.
type APIKeySummary struct {
	ID                 int64   `json:"id"`
	Name               string  `json:"name"`
	Status             string  `json:"status"`
	KeyHint            string  `json:"key_hint"`
	Quota              float64 `json:"quota"`
	QuotaUsed          float64 `json:"quota_used"`
	ExpiresAt          string  `json:"expires_at,omitempty"`
	CurrentConcurrency int     `json:"current_concurrency"`
	Usage5h            float64 `json:"usage_5h"`
	Usage1d            float64 `json:"usage_1d"`
	Usage7d            float64 `json:"usage_7d"`
}

type APIKeySelectionResult struct {
	Selected   APIKeySummary     `json:"selected"`
	Connection ConnectionSummary `json:"connection"`
}

type DeviceSummary struct {
	DeviceID        string   `json:"device_id"`
	ClientID        string   `json:"client_id"`
	DeviceName      string   `json:"device_name"`
	Scopes          []string `json:"scopes"`
	Audience        string   `json:"audience"`
	ProtectionLevel string   `json:"protection_level"`
	CreatedAt       string   `json:"created_at"`
	LastSeenAt      string   `json:"last_seen_at"`
	RevokedAt       string   `json:"revoked_at,omitempty"`
}

type CheckoutSessionInput struct {
	Amount                    float64 `json:"amount"`
	PaymentType               string  `json:"payment_type"`
	OrderType                 string  `json:"order_type,omitempty"`
	PlanID                    int64   `json:"plan_id,omitempty"`
	UpgradeFromSubscriptionID int64   `json:"upgrade_from_subscription_id,omitempty"`
}

type ImageHistoryQueryInput struct {
	Cursor string `json:"cursor,omitempty"`
	Status string `json:"status,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// LocalImageAssetSummary is the only image-library shape exposed to the
// renderer.  The backing file path is deliberately kept inside imagestore and
// is never included in a Wails binding or JSON response.
type LocalImageAssetSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MimeType  string `json:"mime_type"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256,omitempty"`
	CreatedAt string `json:"created_at"`
}

// IntegrateToolConfig merges the securely stored API key into the native
// Codex/Claude configuration. It is deliberately an explicit user action; no
// background process writes credentials to tool files.
func (a *App) IntegrateToolConfig(input ToolConfigInput) (ToolConfigResult, error) {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx, cancel := context.WithTimeout(a.appContext(), 15*time.Second)
	defer cancel()
	tool := strings.ToLower(strings.TrimSpace(input.Tool))
	if tool != string(configwriter.ToolCodex) && tool != string(configwriter.ToolClaude) {
		return ToolConfigResult{}, configwriter.ErrUnsupportedTool
	}
	config, err := a.config.Load(ctx)
	if err != nil {
		return ToolConfigResult{}, err
	}
	if strings.TrimSpace(config.SiteURL) == "" {
		config.SiteURL = siteclient.OfficialSiteURL
	}
	if !siteclient.IsOfficialSiteURL(config.SiteURL) {
		return ToolConfigResult{}, errors.New("拒绝为非官方站点写入客户端配置")
	}
	key, err := a.toolAPIKey(ctx, config, tool)
	if err != nil {
		return ToolConfigResult{}, errors.New("请先保存 API key；设备会话 token 不能直接用于 Codex/Claude 配置")
	}
	baseURL := strings.TrimSpace(input.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(config.GatewayURL)
	}
	if baseURL == "" {
		baseURL = config.SiteURL
	}
	baseURL, err = siteclient.ParseGatewayURL(baseURL)
	if err != nil || !siteclient.IsOfficialSiteURL(baseURL) {
		return ToolConfigResult{}, errors.New("客户端配置地址必须是官方站点")
	}
	result, err := configwriter.IntegrateTool(ctx, configwriter.ToolIntegrationInput{
		Tool: configwriter.Tool(tool), HomeDir: "", BaseURL: baseURL, APIKey: key, Model: input.Model,
	})
	if err != nil {
		return ToolConfigResult{}, err
	}
	converted := toolConfigResult(result)
	// Both integrations expose the same terminal helper. It keeps the secret
	// out of argv and shell startup files; Claude's native settings file still
	// contains its auth value for compatibility, so the UI warning remains.
	if plan, planErr := newToolLaunchPlan(tool); planErr == nil {
		converted.Launch = &plan
		if tool == string(configwriter.ToolCodex) {
			converted.Warnings = append(converted.Warnings, "启动 Codex 时请使用下方命令；客户端会从系统安全存储按需注入 SUB2API_API_KEY，不会修改 shell 配置。")
		} else {
			converted.Warnings = append(converted.Warnings, "启动 Claude Code 时可使用下方命令；密钥仍会保留在 settings.json 以兼容官方客户端，请仅在受信终端执行。")
		}
	} else {
		converted.Warnings = append(converted.Warnings, "未能生成安全启动指令，请在受信终端中启动本地客户端并按配置设置其认证环境变量。")
	}
	return converted, nil
}

func toolAPIKeyRef(config configwriter.ConnectionConfig, tool string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case string(configwriter.ToolCodex):
		ref := strings.TrimSpace(config.CodexAPIKeyRef)
		if ref == "" {
			// An empty purpose binding is meaningful: use the explicitly selected
			// image/gateway key instead of probing the fixed Codex slot.  The slot
			// may still contain a value from a previous account when keyring
			// deletion was interrupted.
			return "", nil
		}
		if ref != codexAPIKeyRef || config.CodexAPIKeyID <= 0 {
			return "", errors.New("Codex API key 引用或选择无效，请重新选择")
		}
		return ref, nil
	case string(configwriter.ToolClaude):
		ref := strings.TrimSpace(config.ClaudeAPIKeyRef)
		if ref == "" {
			return "", nil
		}
		if ref != claudeAPIKeyRef || config.ClaudeAPIKeyID <= 0 {
			return "", errors.New("Claude API key 引用或选择无效，请重新选择")
		}
		return ref, nil
	default:
		return "", configwriter.ErrUnsupportedTool
	}
}

func toolAPIKeyID(config configwriter.ConnectionConfig, tool string) int64 {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case string(configwriter.ToolCodex):
		return config.CodexAPIKeyID
	case string(configwriter.ToolClaude):
		return config.ClaudeAPIKeyID
	default:
		return 0
	}
}

// toolAPIKey resolves the key for a local integration without allowing a
// stale keyring entry to cross an account boundary. Device sessions must
// re-read the selected key through the current proof-bound account session;
// API-key-only sessions may use a purpose-specific reference only when both
// its fixed reference and selected id are present.  Otherwise they use the
// current image/gateway key reference; they never probe an unbound purpose
// slot that could contain a previous account's secret.
func (a *App) toolAPIKey(ctx context.Context, config configwriter.ConnectionConfig, tool string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(config.AuthMode))
	deviceMode := isUsableDeviceSessionConfig(config)
	// An explicit device mode (or a legacy empty-mode record carrying a refresh
	// reference) must never silently fall back to an API-key slot when one of the
	// sender-constrained references is stale or tampered.  API-key mode is the
	// only mode allowed to ignore orphaned refresh metadata after a failed
	// account transition.
	if !deviceMode && (mode == "device" || (mode == "" && strings.TrimSpace(config.RefreshTokenRef) != "")) {
		return "", errors.New("设备会话配置无效，请重新授权")
	}
	if deviceMode {
		id := toolAPIKeyID(config, tool)
		if id <= 0 {
			return "", errors.New("当前设备会话尚未为该工具选择 API key")
		}
		client, token, err := a.loadClientAndSession(ctx)
		if err != nil {
			return "", err
		}
		key, err := client.GetAPIKey(ctx, token, id)
		if err != nil || key.ID != id || !usableAPIKey(key, time.Now().UTC()) || strings.TrimSpace(key.Key) == "" {
			return "", errors.New("所选 API key 已停用、过期或不属于当前账户")
		}
		return strings.TrimSpace(key.Key), nil
	}
	keyRef, err := toolAPIKeyRef(config, tool)
	if err != nil {
		return "", err
	}
	if keyRef == "" {
		if strings.TrimSpace(config.APIKeyRef) != apiKeyRef {
			return "", errors.New("当前连接尚未选择可用于该工具的 API key")
		}
		keyRef = apiKeyRef
	}
	key, err := a.secrets.Get(ctx, keyRef)
	if err != nil {
		return "", err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", securestore.ErrNotFound
	}
	return key, nil
}

// OpenKeysPage opens the fixed first-party key-management page. Creation and
// mutation of API keys remain in the browser so the desktop grant only needs
// read access to existing keys.
func (a *App) OpenKeysPage() error {
	wailsruntime.BrowserOpenURL(a.appContext(), siteclient.OfficialSiteURL+"/keys")
	return nil
}

// GetToolConfigPaths returns the paths that the one-click action will touch.
// It is metadata only and never reads or returns file contents.
func (a *App) GetToolConfigPaths(tool string) (map[string]string, error) {
	if strings.TrimSpace(tool) == "" {
		return nil, configwriter.ErrUnsupportedTool
	}
	return configwriter.ToolPaths(configwriter.Tool(strings.ToLower(strings.TrimSpace(tool))), "")
}

// RestoreToolConfig restores a backup generated by IntegrateToolConfig. The
// target path is derived from the selected tool, preventing a renderer from
// using this binding to overwrite an arbitrary local file.
func (a *App) RestoreToolConfig(input ToolConfigRestoreInput) (ToolConfigRestoreResult, error) {
	ctx, cancel := context.WithTimeout(a.appContext(), 15*time.Second)
	defer cancel()
	tool := configwriter.Tool(strings.ToLower(strings.TrimSpace(input.Tool)))
	paths, err := configwriter.ToolPaths(tool, "")
	if err != nil {
		return ToolConfigRestoreResult{}, err
	}
	backupPath := strings.TrimSpace(input.BackupPath)
	if backupPath == "" {
		return ToolConfigRestoreResult{}, configwriter.ErrInvalidToolPath
	}
	allowed := false
	for _, target := range paths {
		prefix := target + ".sub2api-"
		if strings.HasPrefix(filepath.Clean(backupPath), prefix) && strings.HasSuffix(backupPath, ".bak") {
			allowed = true
			break
		}
	}
	if !allowed {
		return ToolConfigRestoreResult{}, configwriter.ErrInvalidToolPath
	}
	// A tool has one or two files. Resolve the backup's original target by its
	// prefix instead of trusting a renderer-supplied destination.
	var targetPath string
	for _, candidate := range paths {
		if strings.HasPrefix(filepath.Clean(backupPath), candidate+".sub2api-") {
			targetPath = candidate
			break
		}
	}
	if targetPath == "" {
		return ToolConfigRestoreResult{}, configwriter.ErrInvalidToolPath
	}
	previous, err := configwriter.RestoreToolFile(ctx, backupPath, targetPath)
	if err != nil {
		return ToolConfigRestoreResult{}, err
	}
	return ToolConfigRestoreResult{Tool: string(tool), TargetPath: targetPath, PreviousBackupPath: previous.BackupPath}, nil
}

func toolConfigResult(result configwriter.ToolIntegrationResult) ToolConfigResult {
	files := make([]ToolConfigFile, 0, len(result.Files))
	for _, file := range result.Files {
		files = append(files, ToolConfigFile{Path: file.Path, BackupPath: file.BackupPath, Changed: file.Changed, ContainsSecret: file.ContainsSecret})
	}
	return ToolConfigResult{Tool: string(result.Tool), Files: files, Warnings: append([]string(nil), result.Warnings...), CompletedAt: result.CompletedAt.Format(time.RFC3339)}
}

type ProbeResult struct {
	Reachable  bool   `json:"reachable"`
	SiteName   string `json:"site_name,omitempty"`
	GatewayURL string `json:"gateway_url,omitempty"`
	APIBaseURL string `json:"api_base_url,omitempty"`
	CheckedAt  string `json:"checked_at"`
	Message    string `json:"message,omitempty"`
}

func (a *App) SaveConnection(input ConnectionInput) (ConnectionSummary, error) {
	a.accountMu.Lock()
	defer a.accountMu.Unlock()
	ctx := a.appContext()
	key := strings.TrimSpace(input.APIKey)
	if key == "" {
		return ConnectionSummary{}, siteclient.ErrMissingAPIKey
	}
	siteURL, err := siteclient.ParseGatewayURL(input.SiteURL)
	if err != nil {
		return ConnectionSummary{}, err
	}
	if !siteclient.IsOfficialSiteURL(siteURL) {
		return ConnectionSummary{}, errors.New("为保护账号安全，桌面客户端只连接官方站点")
	}
	gatewayURL := strings.TrimSpace(input.GatewayURL)
	if gatewayURL != "" {
		gatewayURL, err = siteclient.ParseGatewayURL(gatewayURL)
		if err != nil {
			return ConnectionSummary{}, err
		}
		if !siteclient.IsOfficialSiteURL(gatewayURL) {
			return ConnectionSummary{}, errors.New("为避免 API key 被转发到未知主机，Gateway 也必须使用官方站点")
		}
	}
	client, err := siteclient.New(siteURL, gatewayURL)
	if err != nil {
		return ConnectionSummary{}, err
	}
	oldKey, oldErr := a.secrets.Get(ctx, apiKeyRef)
	previousConfig, previousConfigErr := a.config.Load(ctx)
	if previousConfigErr != nil {
		return ConnectionSummary{}, fmt.Errorf("读取现有连接配置失败，已保留当前连接: %w", previousConfigErr)
	}
	// Account transitions must revoke the old sender-constrained device session
	// before any new secret or metadata is written. If the remote revocation
	// cannot be completed, fail closed and leave the current connection intact
	// so the user can retry or revoke it from the web device-management page.
	deviceSessionRevoked := false
	if isDeviceSessionConfig(previousConfig) {
		if err := a.revokeStoredDeviceSession(ctx, previousConfig); err != nil {
			return ConnectionSummary{}, fmt.Errorf("撤销旧设备会话失败，已保留当前连接: %w", err)
		}
		deviceSessionRevoked = true
	}
	// A raw API key is the only account identity available in API-key mode. If
	// the user replaces it, purpose-specific keyring entries from the previous
	// account must not survive and later power Codex/Claude with the wrong
	// account. Preserve those entries only when the exact image key is unchanged
	// and there is no active device session boundary.
	preservePurposeKeys := oldErr == nil && oldKey == key &&
		!strings.EqualFold(strings.TrimSpace(previousConfig.AuthMode), "device") &&
		strings.TrimSpace(previousConfig.RefreshTokenRef) == ""
	if err := a.secrets.Set(ctx, apiKeyRef, key); err != nil {
		if deviceSessionRevoked {
			cleanupErr := a.clearLocalStateAfterRevokedSession(ctx)
			return ConnectionSummary{}, errors.Join(
				fmt.Errorf("旧设备会话已撤销，但新 API key 未能保存；本地连接已清理，请重试: %w", err),
				cleanupErr,
			)
		}
		return ConnectionSummary{}, fmt.Errorf("store API key: %w", err)
	}
	config := configwriter.ConnectionConfig{
		SchemaVersion:    1,
		SiteURL:          siteURL,
		GatewayURL:       gatewayURL,
		AuthMode:         "api_key",
		APIKeyRef:        apiKeyRef,
		APIKeyID:         0,
		APIKeyHint:       securestore.Mask(key),
		Label:            strings.TrimSpace(input.Label),
		UpdatedAt:        time.Now().UTC(),
		AccountOwnerHash: imagestore.OwnerHashForSubject("api-key-secret:" + key),
		// Purpose-specific selections are retained only for an in-place edit of
		// the same API-key connection. A new key or a device-to-key switch is an
		// account boundary and starts with no Codex/Claude selections.
		CodexAPIKeyID:   0,
		ClaudeAPIKeyID:  0,
		CodexAPIKeyRef:  "",
		ClaudeAPIKeyRef: "",
	}
	if preservePurposeKeys {
		config.CodexAPIKeyID = previousConfig.CodexAPIKeyID
		config.ClaudeAPIKeyID = previousConfig.ClaudeAPIKeyID
		config.CodexAPIKeyRef = fixedOptionalRef(previousConfig.CodexAPIKeyRef, codexAPIKeyRef)
		config.ClaudeAPIKeyRef = fixedOptionalRef(previousConfig.ClaudeAPIKeyRef, claudeAPIKeyRef)
	}
	if config.GatewayURL == "" {
		config.GatewayURL = config.SiteURL
	}
	client, err = siteclient.New(config.SiteURL, config.GatewayURL)
	if err != nil {
		if deviceSessionRevoked {
			cleanupErr := a.clearLocalStateAfterRevokedSession(ctx)
			return ConnectionSummary{}, errors.Join(
				fmt.Errorf("旧设备会话已撤销，但新连接地址无效；本地连接已清理，请重试: %w", err),
				cleanupErr,
			)
		}
		return ConnectionSummary{}, err
	}
	if err := a.config.Save(ctx, config); err != nil {
		if deviceSessionRevoked {
			cleanupErr := a.clearLocalStateAfterRevokedSession(ctx)
			return ConnectionSummary{}, errors.Join(
				fmt.Errorf("旧设备会话已撤销，但新连接配置未能保存；本地连接已清理，请重试: %w", err),
				cleanupErr,
			)
		}
		// Restore the previous account material and verify both sides of the
		// rollback.  A keyring write can fail independently of the atomic JSON
		// writer; silently ignoring that error would leave the old connection
		// metadata paired with the new account's secret.  If either the secret or
		// metadata cannot be proved to be back in its original state, clear all
		// fixed local credentials and force an explicit re-authorization.
		if rollbackErr := a.rollbackAPIKeyConnection(ctx, previousConfig, oldKey, oldErr); rollbackErr != nil {
			return ConnectionSummary{}, errors.Join(
				fmt.Errorf("保存新连接失败且无法安全恢复旧连接；本地连接已清理，请重新配置: %w", err),
				rollbackErr,
			)
		}
		return ConnectionSummary{}, err
	}
	// The account boundary is now committed. Invalidate any pending flow so an
	// older browser approval cannot overwrite this newly selected connection.
	a.clearPendingDeviceAuthorizations()
	if !preservePurposeKeys {
		// The metadata no longer references old purpose keys. Best-effort cleanup
		// keeps a keyring backend outage from blocking a connection switch while
		// preventing the desktop app from using those orphaned values.
		_ = a.secrets.Delete(ctx, codexAPIKeyRef)
		_ = a.secrets.Delete(ctx, claudeAPIKeyRef)
	}
	// API-key mode supersedes a previous device session. Do not leave a stale
	// refresh token in the keychain after the user deliberately switched modes.
	_ = a.secrets.Delete(ctx, accessTokenRef)
	_ = a.secrets.Delete(ctx, refreshTokenRef)
	_ = a.secrets.Delete(ctx, dpopKeyRef)
	a.mu.Lock()
	a.client = client
	a.sessionAccessToken = ""
	a.sessionAccessExpiresAt = time.Time{}
	a.mu.Unlock()
	a.startTaskRecovery()
	return a.connectionSummary(ctx, config), nil
}

func (a *App) GetConnection() (ConnectionSummary, error) {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx := a.appContext()
	config, err := a.config.Load(ctx)
	if err != nil {
		return ConnectionSummary{}, err
	}
	if strings.TrimSpace(config.SiteURL) != "" && !siteclient.IsOfficialSiteURL(config.SiteURL) {
		return ConnectionSummary{}, errors.New("检测到旧配置指向非官方站点，请重新连接官方站点")
	}
	if strings.TrimSpace(config.GatewayURL) != "" && !siteclient.IsOfficialSiteURL(config.GatewayURL) {
		return ConnectionSummary{}, errors.New("检测到旧 Gateway 指向非官方站点，请重新配置")
	}
	return a.connectionSummary(ctx, config), nil
}

// ListAPIKeys returns metadata for keys owned by the signed-in account. The
// full secret is deliberately stripped before the result crosses the Wails
// boundary; use SelectAPIKey to place one key into the OS keyring.
func (a *App) ListAPIKeys() ([]APIKeySummary, error) {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx, cancel := context.WithTimeout(a.appContext(), 20*time.Second)
	defer cancel()
	page, err := runSessionRequest(a, ctx, func(client *siteclient.HTTPClient, token string) (siteclient.APIKeyPage, error) {
		// Ask the server for active keys first; the local expiry check below
		// remains necessary because an active row may carry an expired
		// expires_at value until the next server reconciliation.
		return client.ListAPIKeys(ctx, token, 1, 100, "active")
	})
	if err != nil {
		return nil, err
	}
	result := make([]APIKeySummary, 0, len(page.Items))
	now := time.Now().UTC()
	for _, key := range page.Items {
		if !usableAPIKey(key, now) {
			continue
		}
		result = append(result, apiKeySummary(key))
	}
	return result, nil
}

// SelectAPIKey fetches one owned key over the scoped device session and writes
// it to the platform keyring. The connection metadata stores only the stable
// reference and a masked hint; the selected key can then power image/gateway
// calls while the device session remains available for account operations.
func (a *App) SelectAPIKey(id int64) (APIKeySelectionResult, error) {
	return a.SelectAPIKeyForPurpose("images", id)
}

// SelectAPIKeyForPurpose selects an owned key for one integration surface.
// The server remains the source of truth for ownership/status; the desktop
// metadata stores only the key id and a fixed keyring reference.
func (a *App) SelectAPIKeyForPurpose(purpose string, id int64) (APIKeySelectionResult, error) {
	a.accountMu.Lock()
	defer a.accountMu.Unlock()
	ctx, cancel := context.WithTimeout(a.appContext(), 20*time.Second)
	defer cancel()
	purpose = strings.ToLower(strings.TrimSpace(purpose))
	ref, err := apiKeyReferenceForPurpose(purpose)
	if err != nil {
		return APIKeySelectionResult{}, err
	}
	if id <= 0 {
		return APIKeySelectionResult{}, errors.New("API key ID 无效")
	}
	key, err := runSessionRequest(a, ctx, func(client *siteclient.HTTPClient, token string) (siteclient.APIKey, error) {
		return client.GetAPIKey(ctx, token, id)
	})
	if err != nil {
		return APIKeySelectionResult{}, err
	}
	if !usableAPIKey(key, time.Now().UTC()) {
		return APIKeySelectionResult{}, errors.New("API key 已停用或已过期，请选择仍有效的密钥")
	}
	if key.ID != id {
		return APIKeySelectionResult{}, errors.New("服务端返回的 API key 与请求不匹配")
	}
	secret := strings.TrimSpace(key.Key)
	if secret == "" {
		return APIKeySelectionResult{}, errors.New("服务端未返回可用 API key")
	}
	oldSecret, oldErr := a.secrets.Get(ctx, ref)
	if err := a.secrets.Set(ctx, ref, secret); err != nil {
		return APIKeySelectionResult{}, fmt.Errorf("保存 API key: %w", err)
	}
	config, err := a.config.Load(ctx)
	if err != nil {
		restoreSecret(ctx, a.secrets, ref, oldSecret, oldErr)
		return APIKeySelectionResult{}, err
	}
	if err := validateOfficialConfig(&config); err != nil {
		restoreSecret(ctx, a.secrets, ref, oldSecret, oldErr)
		return APIKeySelectionResult{}, err
	}
	mode := strings.ToLower(strings.TrimSpace(config.AuthMode))
	deviceMode := isUsableDeviceSessionConfig(config)
	// Selecting a server-owned key is only valid through the current
	// proof-bound device session.  In particular, do not infer device mode
	// from an arbitrary refresh-token reference: an interrupted API-key switch
	// may leave that metadata behind while the old credential is already
	// revoked.  Failing before committing the newly fetched secret keeps the
	// local keyring and owner partition aligned with the active account mode.
	if !deviceMode && (mode == "device" || (mode == "" && strings.TrimSpace(config.RefreshTokenRef) != "")) {
		restoreSecret(ctx, a.secrets, ref, oldSecret, oldErr)
		return APIKeySelectionResult{}, errors.New("设备会话配置无效，请重新授权")
	}
	if !deviceMode {
		restoreSecret(ctx, a.secrets, ref, oldSecret, oldErr)
		return APIKeySelectionResult{}, errors.New("当前连接不是可用的设备会话")
	}
	switch purpose {
	case "images":
		config.APIKeyRef = apiKeyRef
		config.APIKeyID = key.ID
		config.APIKeyHint = securestore.Mask(secret)
		// Selecting an image key never changes the account mode.  The server
		// owned key was fetched through the current proof-bound device session;
		// keep that session as the account/billing credential and let task
		// ownership resolve from the confirmed profile on the next request.
		config.AccountOwnerHash = ""
	case "codex":
		config.CodexAPIKeyRef = codexAPIKeyRef
		config.CodexAPIKeyID = key.ID
	case "claude":
		config.ClaudeAPIKeyRef = claudeAPIKeyRef
		config.ClaudeAPIKeyID = key.ID
	}
	config.UpdatedAt = time.Now().UTC()
	if err := a.config.Save(ctx, config); err != nil {
		restoreSecret(ctx, a.secrets, ref, oldSecret, oldErr)
		return APIKeySelectionResult{}, err
	}
	return APIKeySelectionResult{Selected: apiKeySummary(key), Connection: a.connectionSummary(ctx, config)}, nil
}

func apiKeyReferenceForPurpose(purpose string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(purpose)) {
	case "", "images", "image", "gateway":
		return apiKeyRef, nil
	case "codex":
		return codexAPIKeyRef, nil
	case "claude", "claude_code":
		return claudeAPIKeyRef, nil
	default:
		return "", errors.New("不支持的 API key 用途")
	}
}

// DeviceAuthorizationInput contains no credential material. The server
// returns a one-time user code; the user approves it in the official site
// browser session, then the desktop polls with its in-memory proof.
type DeviceAuthorizationInput struct {
	DeviceName string   `json:"device_name"`
	Scopes     []string `json:"scopes,omitempty"`
}

type DeviceAuthorizationView struct {
	RequestID               string `json:"request_id"`
	UserCode                string `json:"user_code"`
	VerificationURL         string `json:"verification_url"`
	VerificationURLComplete string `json:"verification_url_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	Scope                   string `json:"scope"`
	Audience                string `json:"audience"`
}

type DeviceAuthorizationStatus struct {
	RequestID  string `json:"request_id"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	ExpiresIn  int    `json:"expires_in,omitempty"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
}

// BeginDeviceAuthorization starts the first-party device flow against the
// pinned official origin. It does not open a browser implicitly; the UI calls
// OpenDeviceVerification after showing the code so the user can verify the
// destination first.
func (a *App) BeginDeviceAuthorization(input DeviceAuthorizationInput) (DeviceAuthorizationView, error) {
	// Serialize the complete authorization start with account transitions. The
	// request creates a proof-bound pending flow; allowing SaveConnection or
	// ClearConnection to interleave could leave a stale flow able to overwrite
	// the connection after the user switches accounts.
	a.accountMu.Lock()
	defer a.accountMu.Unlock()
	ctx, cancel := context.WithTimeout(a.appContext(), 15*time.Second)
	defer cancel()
	client, err := siteclient.New(siteclient.OfficialSiteURL, "")
	if err != nil {
		return DeviceAuthorizationView{}, err
	}
	proof, err := siteclient.NewDeviceProof()
	if err != nil {
		return DeviceAuthorizationView{}, err
	}
	auth, err := client.BeginDeviceAuthorization(ctx, siteclient.DeviceAuthorizationRequest{
		ClientID:        siteclient.DesktopClientID,
		DeviceName:      strings.TrimSpace(input.DeviceName),
		Scopes:          append([]string(nil), input.Scopes...),
		Audience:        siteclient.DesktopAudience,
		ProtectionLevel: securestore.ProtectionLevel(a.secrets),
		Proof:           proof,
	})
	if err != nil {
		return DeviceAuthorizationView{}, err
	}
	verificationURL, err := client.OpenVerificationURL(auth)
	if err != nil {
		return DeviceAuthorizationView{}, err
	}
	requestID, err := randomRequestID()
	if err != nil {
		return DeviceAuthorizationView{}, err
	}
	until := time.Now().UTC().Add(time.Duration(auth.ExpiresIn) * time.Second)
	a.mu.Lock()
	if a.pending == nil {
		a.pending = make(map[string]*pendingDeviceAuthorization)
	}
	a.pending[requestID] = &pendingDeviceAuthorization{client: client, proof: proof, auth: auth, url: verificationURL, until: until}
	a.mu.Unlock()
	return DeviceAuthorizationView{
		RequestID: requestID, UserCode: auth.UserCode, VerificationURL: verificationURL,
		VerificationURLComplete: verificationURL, ExpiresIn: auth.ExpiresIn,
		Interval: auth.Interval, Scope: auth.Scope, Audience: auth.Audience,
	}, nil
}

// OpenDeviceVerification opens the server-provided HTTPS page using the
// platform browser. The URL has already been resolved against the pinned site
// origin and is not accepted from arbitrary frontend input.
func (a *App) OpenDeviceVerification(requestID string) error {
	pending, err := a.pendingDevice(requestID)
	if err != nil {
		return err
	}
	wailsruntime.BrowserOpenURL(a.appContext(), pending.url)
	return nil
}

// PollDeviceAuthorization performs one RFC 8628-style poll. Pending and
// terminal OAuth states are returned as status values (not opaque errors) so
// the Vue UI can keep polling without logging noisy failures.
func (a *App) PollDeviceAuthorization(requestID string) (DeviceAuthorizationStatus, error) {
	a.accountMu.Lock()
	defer a.accountMu.Unlock()
	pending, err := a.pendingDevice(requestID)
	if err != nil {
		return DeviceAuthorizationStatus{RequestID: requestID, Status: "expired", Message: err.Error()}, nil
	}
	if !pending.until.IsZero() && time.Now().UTC().After(pending.until) {
		a.removePendingDevice(requestID)
		return DeviceAuthorizationStatus{RequestID: requestID, Status: "expired", Message: "授权码已过期，请重新开始"}, nil
	}
	ctx, cancel := context.WithTimeout(a.appContext(), 15*time.Second)
	defer cancel()
	token, err := pending.client.ExchangeDeviceAuthorization(ctx, siteclient.DeviceTokenRequest{
		DeviceCode: pending.auth.DeviceCode,
		ClientID:   siteclient.DesktopClientID,
		Audience:   siteclient.DesktopAudience,
		Proof:      pending.proof,
	})
	if err != nil {
		if siteclient.IsAuthorizationPending(err) {
			remaining := int(time.Until(pending.until).Round(time.Second) / time.Second)
			if remaining < 0 {
				remaining = 0
			}
			return DeviceAuthorizationStatus{RequestID: requestID, Status: "pending", ExpiresIn: remaining, Message: "等待浏览器确认"}, nil
		}
		if siteclient.IsAuthorizationDenied(err) {
			a.removePendingDevice(requestID)
			return DeviceAuthorizationStatus{RequestID: requestID, Status: "denied", Message: err.Error()}, nil
		}
		return DeviceAuthorizationStatus{RequestID: requestID, Status: "error", Message: err.Error()}, err
	}
	// A successful authorization can replace an already enrolled device on the
	// same workstation. Revoke the old sender-constrained session before
	// overwriting its refresh token/private-session metadata. If this boundary
	// fails, best-effort revoke the newly issued session and leave the old local
	// credentials untouched so the user can retry safely.
	previousConfig, previousConfigErr := a.config.Load(ctx)
	if previousConfigErr != nil {
		// The token exchange consumed the one-time authorization code. Use a
		// detached cleanup context so a canceled poll cannot strand the newly
		// issued refresh session on the server.
		if compensationErr := a.revokeIssuedDeviceSession(ctx, pending.client, token.RefreshToken); compensationErr != nil {
			previousConfigErr = errors.Join(previousConfigErr, fmt.Errorf("撤销新设备会话失败: %w", compensationErr))
		}
		a.removePendingDevice(requestID)
		return DeviceAuthorizationStatus{RequestID: requestID, Status: "error", Message: previousConfigErr.Error()}, previousConfigErr
	}
	previousDeviceRevoked := false
	if isDeviceSessionConfig(previousConfig) {
		if revokeErr := a.revokeStoredDeviceSession(ctx, previousConfig); revokeErr != nil {
			compensationErr := a.revokeIssuedDeviceSession(ctx, pending.client, token.RefreshToken)
			a.removePendingDevice(requestID)
			if compensationErr != nil {
				revokeErr = errors.Join(revokeErr, fmt.Errorf("撤销新设备会话失败: %w", compensationErr))
			}
			return DeviceAuthorizationStatus{RequestID: requestID, Status: "error", Message: fmt.Sprintf("撤销旧设备会话失败，已保留当前连接: %v", revokeErr)}, revokeErr
		}
		previousDeviceRevoked = true
	}
	if err := a.saveDeviceTokens(ctx, token, pending.proof); err != nil {
		// The authorization code has already been consumed. Revoke the newly
		// issued server session even when the poll context is canceled. If the
		// old device session was already revoked, its local metadata is no longer
		// recoverable and must be cleared rather than left pointing at a dead
		// refresh token. Otherwise only remove the newly written token slots.
		cleanupErr := a.revokeIssuedDeviceSession(ctx, pending.client, token.RefreshToken)
		if previousDeviceRevoked {
			cleanupErr = errors.Join(cleanupErr, a.clearLocalStateAfterRevokedSession(ctx))
		} else {
			cleanupErr = errors.Join(cleanupErr, a.clearIssuedDeviceTokens(ctx))
		}
		a.removePendingDevice(requestID)
		return DeviceAuthorizationStatus{RequestID: requestID, Status: "error", Message: err.Error()}, errors.Join(err, cleanupErr)
	}
	if err := a.saveDeviceConnection(ctx, token, pending.proof); err != nil {
		// Do not leave an orphaned server session if local metadata cannot be
		// committed. The same account-boundary rule applies here as above.
		cleanupErr := a.revokeIssuedDeviceSession(ctx, pending.client, token.RefreshToken)
		if previousDeviceRevoked {
			cleanupErr = errors.Join(cleanupErr, a.clearLocalStateAfterRevokedSession(ctx))
		} else {
			cleanupErr = errors.Join(cleanupErr, a.clearIssuedDeviceTokens(ctx))
		}
		a.removePendingDevice(requestID)
		return DeviceAuthorizationStatus{RequestID: requestID, Status: "error", Message: err.Error()}, errors.Join(err, cleanupErr)
	}
	a.removePendingDevice(requestID)
	a.startTaskRecovery()
	status := DeviceAuthorizationStatus{RequestID: requestID, Status: "authorized", Message: "设备已授权"}
	if token.Device != nil {
		status.DeviceID = token.Device.DeviceID
		status.DeviceName = token.Device.DeviceName
	}
	return status, nil
}

func (a *App) pendingDevice(requestID string) (*pendingDeviceAuthorization, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, errors.New("授权请求不存在")
	}
	a.mu.RLock()
	pending := a.pending[requestID]
	a.mu.RUnlock()
	if pending == nil {
		return nil, siteclient.ErrDeviceProofExpired
	}
	return pending, nil
}

func (a *App) removePendingDevice(requestID string) {
	a.mu.Lock()
	delete(a.pending, strings.TrimSpace(requestID))
	a.mu.Unlock()
}

func (a *App) clearPendingDeviceAuthorizations() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.pending = make(map[string]*pendingDeviceAuthorization)
	a.mu.Unlock()
}

func randomRequestID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate authorization request id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func (a *App) saveDeviceTokens(ctx context.Context, token siteclient.DeviceToken, proof *siteclient.DeviceProof) error {
	return a.saveDeviceTokensWithRefs(ctx, token, proof, accessTokenRef, refreshTokenRef, dpopKeyRef)
}

func (a *App) saveDeviceTokensWithRefs(ctx context.Context, token siteclient.DeviceToken, proof *siteclient.DeviceProof, accessRef, refreshRef, proofRef string) error {
	if a == nil || a.secrets == nil {
		return errors.New("设备凭证存储不可用")
	}
	if strings.TrimSpace(token.AccessToken) == "" || strings.TrimSpace(token.RefreshToken) == "" {
		return errors.New("设备授权响应缺少 token")
	}
	if proof != nil && strings.TrimSpace(token.DPoPNonce) == "" {
		return errors.New("设备授权响应缺少 DPoP nonce")
	}
	accessRef = firstNonEmpty(accessRef, accessTokenRef)
	refreshRef = firstNonEmpty(refreshRef, refreshTokenRef)
	proofRef = firstNonEmpty(proofRef, dpopKeyRef)
	oldRefresh, oldRefreshErr := a.secrets.Get(ctx, refreshRef)
	oldProof, oldProofErr := a.secrets.Get(ctx, proofRef)
	var proofRaw []byte
	var proofErr error
	if proof != nil {
		proof.SetNonce(token.DPoPNonce)
		proofRaw, proofErr = proof.MarshalPrivate()
		if proofErr != nil {
			return fmt.Errorf("serialize device proof: %w", proofErr)
		}
	}
	if err := a.secrets.Set(ctx, refreshRef, token.RefreshToken); err != nil {
		if oldRefreshErr == nil {
			_ = a.secrets.Set(ctx, refreshRef, oldRefresh)
		} else {
			_ = a.secrets.Delete(ctx, refreshRef)
		}
		return fmt.Errorf("保存 refresh token: %w", err)
	}
	if len(proofRaw) > 0 {
		if err := a.secrets.Set(ctx, proofRef, string(proofRaw)); err != nil {
			if oldRefreshErr == nil {
				_ = a.secrets.Set(ctx, refreshRef, oldRefresh)
			} else {
				_ = a.secrets.Delete(ctx, refreshRef)
			}
			if oldProofErr == nil {
				_ = a.secrets.Set(ctx, proofRef, oldProof)
			} else {
				_ = a.secrets.Delete(ctx, proofRef)
			}
			return fmt.Errorf("保存设备密钥: %w", err)
		}
	}
	// Best-effort migration cleanup for pre-release builds that persisted the
	// short-lived access token. The current token exists only in App memory.
	_ = a.secrets.Delete(ctx, accessRef)
	a.setSessionAccessToken(token.AccessToken, token.ExpiresIn)
	return nil
}

func (a *App) saveDeviceConnection(ctx context.Context, token siteclient.DeviceToken, proof *siteclient.DeviceProof) error {
	config, err := a.config.Load(ctx)
	if err != nil {
		return err
	}
	// Device authorization always starts at the pinned first-party origin. Do
	// not let a stale/tampered connection.json choose the host that receives a
	// refresh-capable session. Validate both origins before writing metadata,
	// then canonicalize the site URL for future requests.
	if err := validateOfficialConfig(&config); err != nil {
		return fmt.Errorf("拒绝保存设备会话: %w", err)
	}
	config.SiteURL = siteclient.OfficialSiteURL
	config.GatewayURL = siteclient.OfficialSiteURL
	config.SchemaVersion = 1
	config.AuthMode = "device"
	// API-key secrets are account material, not device material. A user can
	// authorize a different account on the same workstation, so never carry
	// image/Codex/Claude selections across the device-account boundary. The
	// fixed keyring entries are deleted after metadata is committed below; the
	// explicit zeroing here also makes a failed deletion unusable to callers.
	config.APIKeyRef = ""
	config.APIKeyID = 0
	config.APIKeyHint = ""
	config.CodexAPIKeyRef = ""
	config.CodexAPIKeyID = 0
	config.ClaudeAPIKeyRef = ""
	config.ClaudeAPIKeyID = 0
	config.AccountOwnerHash = ""
	config.AccessTokenRef = ""
	config.RefreshTokenRef = refreshTokenRef
	config.DPoPKeyRef = dpopKeyRef
	config.DPoPNonce = token.DPoPNonce
	config.Scope = token.Scope
	if token.Device != nil {
		config.DeviceID = token.Device.DeviceID
		config.ProtectionLevel = token.Device.ProtectionLevel
		if strings.TrimSpace(config.Label) == "" {
			config.Label = token.Device.DeviceName
		}
	}
	config.UpdatedAt = time.Now().UTC()
	if err := a.config.Save(ctx, config); err != nil {
		return err
	}
	for _, ref := range []string{apiKeyRef, codexAPIKeyRef, claudeAPIKeyRef} {
		// Metadata no longer references these values, so a keyring backend
		// failure cannot make them active in the newly authorized account. Keep
		// the session usable and let the next explicit key selection overwrite
		// the slot; no caller is allowed to read an unreferenced entry.
		_ = a.secrets.Delete(ctx, ref)
	}
	client, err := siteclient.New(config.SiteURL, config.GatewayURL)
	if err != nil {
		return err
	}
	if proof != nil {
		client.SetDeviceProof(proof, token.DPoPNonce)
	}
	a.mu.Lock()
	a.client = client
	a.mu.Unlock()
	return nil
}

// revokeStoredDeviceSession sends the official sender-constrained logout for
// the currently persisted device session. It deliberately does not acquire
// accountMu: callers invoke it while holding that lock, and calling the public
// LogoutDevice binding here would deadlock. A missing refresh token is treated
// as an unknown remote state rather than success.
func (a *App) revokeStoredDeviceSession(ctx context.Context, config configwriter.ConnectionConfig) error {
	if a == nil || a.secrets == nil {
		return errors.New("设备凭证存储不可用")
	}
	if !isDeviceSessionConfig(config) {
		return nil
	}
	if err := validateOfficialConfig(&config); err != nil {
		return fmt.Errorf("拒绝向非官方站点发送设备撤销: %w", err)
	}
	refresh, err := a.secrets.Get(ctx, refreshTokenRef)
	if errors.Is(err, securestore.ErrNotFound) {
		// The metadata says a device session exists, but the sender-constrained
		// refresh credential is gone. Treat that as an unknown remote state: a
		// caller performing an account switch must not silently proceed while an
		// old server session might still be active. ClearConnection/LogoutDevice
		// still remove local state and surface this error to the user.
		return errors.New("设备 refresh token 不可用，请先在官方站点撤销该设备")
	}
	if err != nil {
		return fmt.Errorf("读取 refresh token: %w", err)
	}
	if strings.TrimSpace(config.DPoPNonce) == "" {
		// Metadata can lag the live client after a rotated nonce. Use the live
		// value as a last-resort recovery path before declaring the session
		// impossible to revoke.
		config.DPoPNonce = a.currentDPoPNonce()
	}
	proof, err := a.restoreDeviceProof(ctx, config)
	if err != nil {
		return err
	}
	client, err := siteclient.New(siteclient.OfficialSiteURL, siteclient.OfficialSiteURL)
	if err != nil {
		return fmt.Errorf("创建设备撤销客户端: %w", err)
	}
	nonce := strings.TrimSpace(config.DPoPNonce)
	// A protected request may have rotated the nonce in the live client just
	// before an account transition while metadata persistence lagged behind.
	// Prefer that in-memory value when available; otherwise use the persisted
	// nonce restored above. Both values remain bound to the same private key.
	if currentNonce := a.currentDPoPNonce(); currentNonce != "" {
		nonce = currentNonce
	}
	client.SetDeviceProof(proof, nonce)
	logout := a.logoutDeviceSession
	if logout == nil {
		logout = func(callCtx context.Context, callClient *siteclient.HTTPClient, token string) error {
			return callClient.LogoutDevice(callCtx, token)
		}
	}
	if err := logout(ctx, client, refresh); err != nil {
		return err
	}
	return nil
}

func (a *App) currentDPoPNonce() string {
	if a == nil {
		return ""
	}
	a.mu.RLock()
	client := a.client
	a.mu.RUnlock()
	if client == nil {
		return ""
	}
	return strings.TrimSpace(client.DPoPNonce())
}

// clearLocalStateAfterRevokedSession is used only after the server has
// confirmed revocation but the replacement connection could not be committed.
// Leaving the old metadata around would make a dead refresh token look like a
// live session on the next launch; clearing all fixed credential slots and the
// metadata is safer and gives the user a clean retry point.
func (a *App) clearLocalStateAfterRevokedSession(ctx context.Context) error {
	if a == nil {
		return errors.New("应用状态不可用")
	}
	cleanupCtx, cancel := detachedCleanupContext(ctx)
	defer cancel()
	var cleanupErr error
	if a.config != nil {
		if err := a.config.Clear(cleanupCtx); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if a.secrets == nil {
		cleanupErr = errors.Join(cleanupErr, errors.New("设备凭证存储不可用"))
	} else {
		for _, ref := range []string{apiKeyRef, codexAPIKeyRef, claudeAPIKeyRef, accessTokenRef, refreshTokenRef, dpopKeyRef} {
			if err := a.secrets.Delete(cleanupCtx, ref); err != nil {
				cleanupErr = errors.Join(cleanupErr, err)
			}
		}
	}
	a.mu.Lock()
	a.client = nil
	a.sessionAccessToken = ""
	a.sessionAccessExpiresAt = time.Time{}
	a.mu.Unlock()
	a.clearPendingDeviceAuthorizations()
	return cleanupErr
}

// clearIssuedDeviceTokens removes only the credentials written by a newly
// exchanged device token. It is used when there was no prior device session to
// revoke, so an existing API-key connection and its metadata remain intact.
func (a *App) clearIssuedDeviceTokens(ctx context.Context) error {
	if a == nil {
		return errors.New("应用状态不可用")
	}
	cleanupCtx, cancel := detachedCleanupContext(ctx)
	defer cancel()
	if a.secrets == nil {
		a.clearSessionAccessToken()
		return errors.New("设备凭证存储不可用")
	}
	var cleanupErr error
	for _, ref := range []string{accessTokenRef, refreshTokenRef, dpopKeyRef} {
		if err := a.secrets.Delete(cleanupCtx, ref); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	a.clearSessionAccessToken()
	return cleanupErr
}

// revokeIssuedDeviceSession compensates a successful token exchange when the
// local commit cannot complete. It deliberately owns a detached, bounded
// context: the original poll context may have been canceled by the UI while
// the server-side refresh family still needs to be revoked.
func (a *App) revokeIssuedDeviceSession(ctx context.Context, client *siteclient.HTTPClient, refreshToken string) error {
	if strings.TrimSpace(refreshToken) == "" || client == nil {
		return nil
	}
	cleanupCtx, cancel := detachedCleanupContext(ctx)
	defer cancel()
	return client.LogoutDevice(cleanupCtx, refreshToken)
}

// detachedCleanupContext preserves the ability to revoke an issued refresh
// session and remove local credentials after the initiating request is
// canceled. A canceled context is appropriate for the primary operation, but
// it must not turn security cleanup into a best-effort no-op.
func detachedCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), localCleanupTimeout)
}

func (a *App) ClearConnection() error {
	// Clearing credentials is an account transition.  Serialize it with image
	// admission/recovery and other account-bound requests so no in-flight call
	// can retain the previous account while keyring entries are removed.
	a.accountMu.Lock()
	defer a.accountMu.Unlock()
	defer a.clearPendingDeviceAuthorizations()
	ctx := a.appContext()
	storedConfig, loadErr := a.config.Load(ctx)
	var cleanupErr error
	// Never trust connection.json to select a logout destination. The helper
	// validates the official origin and sends the refresh token only with a
	// restored DPoP proof. Local deletion remains best effort even if the remote
	// call fails, and the returned error makes that boundary visible to the UI.
	if loadErr == nil {
		if revokeErr := a.revokeStoredDeviceSession(ctx, storedConfig); revokeErr != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("revoke desktop session: %w", revokeErr))
		}
	}
	if err := a.config.Clear(ctx); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	// References are fixed constants, never renderer/config supplied strings.
	// This prevents a tampered metadata file from turning ClearConnection into
	// an arbitrary keychain deletion primitive.
	for _, ref := range []string{apiKeyRef, codexAPIKeyRef, claudeAPIKeyRef, accessTokenRef, refreshTokenRef, dpopKeyRef} {
		if err := a.secrets.Delete(ctx, ref); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	a.mu.Lock()
	a.client = nil
	a.sessionAccessToken = ""
	a.sessionAccessExpiresAt = time.Time{}
	a.mu.Unlock()
	if loadErr != nil {
		cleanupErr = errors.Join(cleanupErr, loadErr)
	}
	return cleanupErr
}

// LogoutDevice revokes the current device session and clears only credential
// entries. Local image/task history remains available for recovery.
func (a *App) LogoutDevice() error {
	a.accountMu.Lock()
	defer a.accountMu.Unlock()
	defer a.clearPendingDeviceAuthorizations()
	ctx := a.appContext()
	config, configErr := a.config.Load(ctx)
	// Device references are fixed constants. Metadata is user-editable JSON and
	// must never be allowed to select arbitrary keychain entries for deletion.
	refreshRef := refreshTokenRef
	accessRef := accessTokenRef
	proofRef := dpopKeyRef
	var logoutErr error
	if configErr == nil {
		if revokeErr := a.revokeStoredDeviceSession(ctx, config); revokeErr != nil {
			logoutErr = fmt.Errorf("revoke desktop session: %w", revokeErr)
		}
	}
	for _, ref := range []string{accessRef, refreshRef, proofRef} {
		if err := a.secrets.Delete(ctx, ref); err != nil {
			logoutErr = errors.Join(logoutErr, err)
		}
	}
	if configErr == nil {
		config.AuthMode = ""
		config.AccessTokenRef = ""
		config.RefreshTokenRef = ""
		config.DPoPKeyRef = ""
		config.DPoPNonce = ""
		config.DeviceID = ""
		config.ProtectionLevel = ""
		config.Scope = ""
		config.UpdatedAt = time.Now().UTC()
		if config.APIKeyRef == "" {
			_ = a.config.Clear(ctx)
		} else if saveErr := a.config.Save(ctx, config); saveErr != nil {
			logoutErr = errors.Join(logoutErr, saveErr)
		}
	}
	a.mu.Lock()
	a.client = nil
	a.sessionAccessToken = ""
	a.sessionAccessExpiresAt = time.Time{}
	a.mu.Unlock()
	if configErr != nil {
		logoutErr = errors.Join(logoutErr, configErr)
	}
	return logoutErr
}

// ListDevices returns the account's registered desktop devices, including
// revoked records for transparency. Public key material is represented only
// by the server's opaque device id in this binding.
func (a *App) ListDevices() ([]DeviceSummary, error) {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx, cancel := context.WithTimeout(a.appContext(), 20*time.Second)
	defer cancel()
	devices, err := runSessionRequest(a, ctx, func(client *siteclient.HTTPClient, token string) ([]siteclient.DeviceInfo, error) {
		return client.ListDevices(ctx, token)
	})
	if err != nil {
		return nil, err
	}
	result := make([]DeviceSummary, 0, len(devices))
	for _, device := range devices {
		result = append(result, deviceSummary(device))
	}
	return result, nil
}

// RevokeDevice revokes another desktop session. Revoking the current device
// also clears its local tokens after the server confirms the mutation.
func (a *App) RevokeDevice(deviceID string) error {
	a.accountMu.Lock()
	defer a.accountMu.Unlock()
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return errors.New("设备 ID 不能为空")
	}
	ctx, cancel := context.WithTimeout(a.appContext(), 20*time.Second)
	defer cancel()
	current, _ := a.config.Load(ctx)
	// Device revocation is a state-changing operation.  Do not transparently
	// replay it after an arbitrary transport/5xx error: the first request may
	// have reached the server even when its response was lost.  The user can
	// retry explicitly after the access token is rotated on the next action.
	_, err := runSessionMutation(a, ctx, func(client *siteclient.HTTPClient, token string) (struct{}, error) {
		return struct{}{}, client.RevokeDevice(ctx, token, deviceID)
	})
	if err != nil {
		return err
	}
	if strings.TrimSpace(current.DeviceID) == deviceID {
		// The server has already revoked the family; local deletion must not
		// attempt a second network logout that would only return invalid_grant.
		// connection.json is metadata and may be edited by another local
		// process.  Never let its reference fields select arbitrary keyring
		// entries during cleanup; released builds use these fixed names only.
		for _, ref := range []string{accessTokenRef, refreshTokenRef, dpopKeyRef} {
			_ = a.secrets.Delete(ctx, ref)
		}
		if config, loadErr := a.config.Load(ctx); loadErr == nil {
			config.AuthMode = ""
			config.AccessTokenRef = ""
			config.RefreshTokenRef = ""
			config.DPoPKeyRef = ""
			config.DPoPNonce = ""
			config.DeviceID = ""
			config.ProtectionLevel = ""
			config.Scope = ""
			if config.APIKeyRef == "" {
				_ = a.config.Clear(ctx)
			} else {
				_ = a.config.Save(ctx, config)
			}
		}
		a.mu.Lock()
		a.client = nil
		a.sessionAccessToken = ""
		a.sessionAccessExpiresAt = time.Time{}
		a.mu.Unlock()
	}
	return nil
}

func (a *App) ProbeConnection() (ProbeResult, error) {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx, cancel := context.WithTimeout(a.appContext(), 10*time.Second)
	defer cancel()
	config, err := a.config.Load(ctx)
	if err != nil {
		return ProbeResult{}, err
	}
	if strings.TrimSpace(config.SiteURL) == "" {
		config.SiteURL = siteclient.OfficialSiteURL
	}
	if !siteclient.IsOfficialSiteURL(config.SiteURL) {
		return ProbeResult{}, errors.New("为保护账号安全，桌面客户端只连接官方站点")
	}
	client, err := siteclient.New(config.SiteURL, config.GatewayURL)
	if err != nil {
		return ProbeResult{}, err
	}
	settings, err := client.PublicSettings(ctx)
	result := ProbeResult{CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if err != nil {
		result.Message = err.Error()
		return result, err
	}
	result.Reachable = true
	result.SiteName = settings.SiteName
	result.APIBaseURL = settings.APIBaseURL
	result.GatewayURL = config.GatewayURL
	if settings.APIBaseURL != "" && siteclient.IsOfficialSiteURL(settings.APIBaseURL) {
		if gateway, parseErr := siteclient.ParseGatewayURL(settings.APIBaseURL); parseErr == nil {
			result.GatewayURL = gateway
			if config.GatewayURL != gateway {
				config.GatewayURL = gateway
				config.UpdatedAt = time.Now().UTC()
				_ = a.config.Save(ctx, config)
				client, _ = siteclient.New(config.SiteURL, gateway)
			}
		}
	}
	a.mu.Lock()
	a.client = client
	a.lastChecked = time.Now().UTC()
	a.mu.Unlock()
	return result, nil
}

type UsageSummary struct {
	Mode            string  `json:"mode"`
	Status          string  `json:"status,omitempty"`
	PlanName        string  `json:"plan_name,omitempty"`
	Remaining       float64 `json:"remaining"`
	Balance         float64 `json:"balance"`
	Unit            string  `json:"unit"`
	Valid           bool    `json:"valid"`
	StatsAvailable  bool    `json:"stats_available"`
	TotalRequests   int64   `json:"total_requests,omitempty"`
	TotalTokens     int64   `json:"total_tokens,omitempty"`
	TotalCost       float64 `json:"total_cost,omitempty"`
	TotalActualCost float64 `json:"total_actual_cost,omitempty"`
	TodayRequests   int64   `json:"today_requests,omitempty"`
	TodayTokens     int64   `json:"today_tokens,omitempty"`
	TodayCost       float64 `json:"today_cost,omitempty"`
	TodayActualCost float64 `json:"today_actual_cost,omitempty"`
}

// UsageOverview keeps account and selected-key usage separate. A nil entry
// means the corresponding scope/key is unavailable; it is intentionally not
// converted to a numeric zero, which would falsely imply zero consumption.
type UsageOverview struct {
	Account          *UsageSummary `json:"account,omitempty"`
	SelectedKey      *UsageSummary `json:"selected_key,omitempty"`
	AccountReady     bool          `json:"account_ready"`
	SelectedKeyReady bool          `json:"selected_key_ready"`
	AsOf             string        `json:"as_of"`
}

// GetUsageOverview reads both independent usage surfaces when available. One
// failing surface does not hide the other, allowing a revoked key or a missing
// usage scope to be displayed as “暂无数据” instead of zero.
func (a *App) GetUsageOverview() (UsageOverview, error) {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx, cancel := context.WithTimeout(a.appContext(), 20*time.Second)
	defer cancel()
	config, err := a.config.Load(ctx)
	if err != nil {
		return UsageOverview{}, err
	}
	if err := validateOfficialConfig(&config); err != nil {
		return UsageOverview{}, err
	}
	result := UsageOverview{AsOf: time.Now().UTC().Format(time.RFC3339)}
	var firstErr error
	// Selected-key usage is independent from the desktop account session, but
	// the source of the key is not. In device mode loadClientAndKey first asks
	// the current proof-bound session to re-confirm ownership of the configured
	// key ID; it never trusts an orphaned keyring value left by another account.
	if client, key, keyErr := a.loadClientAndKey(ctx); keyErr == nil {
		if usage, usageErr := client.Usage(ctx, key); usageErr == nil {
			result.SelectedKey = usageSummary(usage)
			result.SelectedKeyReady = true
		} else if firstErr == nil {
			firstErr = usageErr
		}
	} else if firstErr == nil {
		// Do not turn an absent image key into a misleading zero snapshot. Keep
		// the error only when no account surface succeeds below.
		firstErr = keyErr
	}
	// Account balance/usage requires the scoped device session. Keep the
	// profile call behind its own error boundary so key usage can still render.
	// A leftover refresh/DPoP pair in the keyring must not turn an API-key
	// connection back into the previous account after a mode switch.  Only a
	// positively identified device configuration may open the account session.
	if isUsableDeviceSessionConfig(config) {
		if _, sessionErr := a.secrets.Get(ctx, refreshTokenRef); sessionErr != nil {
			if firstErr == nil {
				firstErr = sessionErr
			}
		} else if client, token, sessionErr := a.loadClientAndSession(ctx); sessionErr == nil {
			profile, profileErr := client.Profile(ctx, token)
			if profileErr != nil {
				// The first request may have been rejected after the in-memory
				// token was issued (for example, server-side revocation). Force a
				// refresh instead of allowing refreshSession to reuse that token.
				if refreshed, refreshErr := a.refreshSessionAfterFailure(ctx, client, token); refreshErr == nil {
					profile, profileErr = client.Profile(ctx, refreshed)
					if profileErr == nil {
						token = refreshed
					}
				}
			}
			if profileErr == nil {
				value := accountUsageSummary(profile)
				if stats, statsErr := client.AccountUsage(ctx, token); statsErr == nil {
					applyAccountUsageStats(&value, stats)
				}
				result.Account = &value
				result.AccountReady = true
				_ = a.persistSessionNonce(ctx, client)
			} else if firstErr == nil {
				firstErr = profileErr
			}
		} else if firstErr == nil {
			firstErr = sessionErr
		}
	}
	if result.AccountReady || result.SelectedKeyReady {
		return result, nil
	}
	if firstErr != nil {
		return result, firstErr
	}
	return result, errors.New("请先配置 API key 或完成设备授权")
}

func (a *App) GetUsage() (UsageSummary, error) {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx := a.appContext()
	config, configErr := a.config.Load(ctx)
	if configErr != nil {
		return UsageSummary{}, configErr
	}
	if err := validateOfficialConfig(&config); err != nil {
		return UsageSummary{}, err
	}
	// API-key mode exposes gateway quota details. Device mode intentionally
	// reads the account balance through the scoped profile endpoint instead of
	// falling back to a stale API key left in the keyring.
	keyRef := strings.TrimSpace(config.APIKeyRef)
	if isAPIKeyConnectionConfig(config) {
		if keyRef != apiKeyRef {
			return UsageSummary{}, errors.New("请先配置 API key 或完成设备授权")
		}
		if key, keyErr := a.secrets.Get(ctx, keyRef); keyErr == nil {
			client, err := siteclient.New(config.SiteURL, config.GatewayURL)
			if err != nil {
				return UsageSummary{}, err
			}
			usage, err := client.Usage(ctx, key)
			if err != nil {
				return UsageSummary{}, err
			}
			return UsageSummary{
				Mode: usage.Mode, Status: usage.Status, PlanName: usage.PlanName,
				Remaining: usage.Remaining, Balance: usage.Balance, Unit: usage.Unit, Valid: usage.IsValid,
			}, nil
		}
	}
	client, accessToken, err := a.loadClientAndSession(ctx)
	if err != nil {
		return UsageSummary{}, errors.New("请先配置 API key 或完成设备授权")
	}
	profile, err := client.Profile(ctx, accessToken)
	if err != nil {
		if refreshed, refreshErr := a.refreshSessionAfterFailure(ctx, client, accessToken); refreshErr == nil {
			profile, err = client.Profile(ctx, refreshed)
			if err == nil {
				accessToken = refreshed
			}
		}
	}
	if err != nil {
		return UsageSummary{}, err
	}
	if err := a.persistSessionNonce(ctx, client); err != nil {
		return UsageSummary{}, err
	}
	value := accountUsageSummary(profile)
	if stats, statsErr := client.AccountUsage(ctx, accessToken); statsErr == nil {
		applyAccountUsageStats(&value, stats)
	}
	return value, nil
}

func usageSummary(usage siteclient.UsageSummary) *UsageSummary {
	return &UsageSummary{Mode: usage.Mode, Status: usage.Status, PlanName: usage.PlanName, Remaining: usage.Remaining, Balance: usage.Balance, Unit: usage.Unit, Valid: usage.IsValid}
}

func accountUsageSummary(profile siteclient.AccountProfile) UsageSummary {
	return UsageSummary{
		Mode: "account", Status: profile.Status, PlanName: profile.Username,
		Remaining: profile.Balance, Balance: profile.Balance, Unit: "USD",
		Valid: profile.Status == "active" || profile.Status == "",
	}
}

func applyAccountUsageStats(summary *UsageSummary, stats siteclient.AccountUsageStats) {
	if summary == nil || !stats.Available {
		return
	}
	summary.StatsAvailable = true
	summary.TotalRequests = stats.TotalRequests
	summary.TotalTokens = stats.TotalTokens
	summary.TotalCost = stats.TotalCost
	summary.TotalActualCost = stats.TotalActualCost
	summary.TodayRequests = stats.TodayRequests
	summary.TodayTokens = stats.TodayTokens
	summary.TodayCost = stats.TodayCost
	summary.TodayActualCost = stats.TodayActualCost
}

type CheckinResult struct {
	RewardAmount float64 `json:"reward_amount"`
	Balance      float64 `json:"balance"`
	Message      string  `json:"message,omitempty"`
	CheckedInAt  string  `json:"checked_in_at,omitempty"`
}

func (a *App) Checkin() (CheckinResult, error) {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx := a.appContext()
	result, err := runSessionMutation(a, ctx, func(client *siteclient.HTTPClient, token string) (siteclient.CheckinResult, error) {
		result, checkinErr := client.Checkin(ctx, token)
		if checkinErr == nil {
			return result, nil
		}
		if !siteclient.IsHTTPErrorReason(checkinErr, "DAILY_CHECKIN_ALREADY_CHECKED_IN") {
			return siteclient.CheckinResult{}, checkinErr
		}
		// The service reports a repeat click as a conflict to preserve its
		// mutation semantics. Treat that known reason as an idempotent success
		// after reading the authoritative status, without issuing a second write.
		status, statusErr := client.CheckinStatus(ctx, token)
		if statusErr != nil {
			return siteclient.CheckinResult{}, statusErr
		}
		if !status.CheckedIn {
			return siteclient.CheckinResult{}, checkinErr
		}
		return idempotentCheckinResult(status), nil
	})
	if err != nil {
		return CheckinResult{}, err
	}
	return CheckinResult{
		RewardAmount: result.RewardAmount, Balance: result.Balance,
		Message: result.Message, CheckedInAt: result.CheckedInAt,
	}, nil
}

func idempotentCheckinResult(status siteclient.CheckinStatus) siteclient.CheckinResult {
	result := siteclient.CheckinResult{Message: "今日已签到"}
	for _, record := range status.MonthCheckins {
		if strings.TrimSpace(status.Today) == "" || record.Date != status.Today {
			continue
		}
		result.RewardAmount = record.RewardAmount
		if !record.CreatedAt.IsZero() {
			result.CheckedInAt = record.CreatedAt.UTC().Format(time.RFC3339)
		}
		break
	}
	return result
}

// CreateCheckoutSession creates an opaque, short-lived hosted checkout. The
// payment provider interaction remains in the browser; no provider secret is
// persisted or returned by this desktop binding.
func (a *App) CreateCheckoutSession(input CheckoutSessionInput) (siteclient.CheckoutSession, error) {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx, cancel := context.WithTimeout(a.appContext(), 20*time.Second)
	defer cancel()
	client, token, err := a.loadClientAndSession(ctx)
	if err != nil {
		return siteclient.CheckoutSession{}, err
	}
	result, err := client.CreateCheckoutSession(ctx, token, siteclient.CheckoutSessionRequest{
		Amount: input.Amount, PaymentType: input.PaymentType, OrderType: input.OrderType,
		PlanID: input.PlanID, UpgradeFromSubscriptionID: input.UpgradeFromSubscriptionID,
		PaymentSource: "desktop",
	})
	if err != nil {
		return siteclient.CheckoutSession{}, err
	}
	if nonceErr := a.persistSessionNonce(ctx, client); nonceErr != nil {
		return siteclient.CheckoutSession{}, nonceErr
	}
	return result, nil
}

// GetCheckoutSession polls the opaque checkout capability. A failed poll is
// safe to retry once after access-token rotation because it is read-only.
func (a *App) GetCheckoutSession(sessionID string) (siteclient.CheckoutSession, error) {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx, cancel := context.WithTimeout(a.appContext(), 20*time.Second)
	defer cancel()
	return a.getCheckoutSessionWithContext(ctx, sessionID)
}

// OpenCheckout opens only a same-origin URL returned by the official API.
func (a *App) OpenCheckout(sessionID string) error {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx, cancel := context.WithTimeout(a.appContext(), 5*time.Second)
	defer cancel()
	session, err := a.getCheckoutSessionWithContext(ctx, sessionID)
	if err != nil {
		return err
	}
	config, err := a.config.Load(ctx)
	if err != nil {
		return err
	}
	if err := validateOfficialConfig(&config); err != nil {
		return err
	}
	client, err := siteclient.New(config.SiteURL, config.GatewayURL)
	if err != nil {
		return err
	}
	value, err := client.ResolveOfficialURL(session.BrowserURL)
	if err != nil {
		return err
	}
	wailsruntime.BrowserOpenURL(a.appContext(), value)
	return nil
}

// getCheckoutSessionWithContext is used by callers that already hold the
// account read lock. Keeping the lock across the subsequent origin lookup in
// OpenCheckout prevents a checkout session from account A being opened after a
// concurrent SaveConnection switches the local connection to account B.
func (a *App) getCheckoutSessionWithContext(ctx context.Context, sessionID string) (siteclient.CheckoutSession, error) {
	return runSessionRequest(a, ctx, func(client *siteclient.HTTPClient, token string) (siteclient.CheckoutSession, error) {
		return client.GetCheckoutSession(ctx, token, sessionID)
	})
}

type ImageGenerateInput struct {
	Model             string `json:"model"`
	Prompt            string `json:"prompt"`
	N                 int    `json:"n"`
	Size              string `json:"size"`
	Quality           string `json:"quality"`
	Background        string `json:"background"`
	OutputFormat      string `json:"output_format"`
	OutputCompression int    `json:"output_compression"`
}

type ImageEditUpload struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type,omitempty"`
	DataURL     string `json:"data_url,omitempty"`
	FileHandle  string `json:"file_handle,omitempty"`
	// Bytes is an advisory renderer-side size hint used for early limit
	// feedback. The native layer always stats/validates the opaque file handle
	// (or decodes the data URL) again and never trusts this value for security.
	Bytes int64 `json:"bytes,omitempty"`
}

// ImageFileHandle is an opaque, short-lived reference returned by the native
// file picker. The absolute path never crosses the Wails bridge.
type ImageFileHandle struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Bytes       int64  `json:"bytes"`
	ExpiresAt   string `json:"expires_at"`
}

type imageFileHandle struct {
	path        string
	name        string
	contentType string
	bytes       int64
	expiresAt   time.Time
}

type ImageEditInput struct {
	Model             string            `json:"model"`
	Prompt            string            `json:"prompt"`
	N                 int               `json:"n"`
	Size              string            `json:"size"`
	Quality           string            `json:"quality"`
	Background        string            `json:"background"`
	OutputFormat      string            `json:"output_format"`
	OutputCompression int               `json:"output_compression"`
	InputFidelity     string            `json:"input_fidelity"`
	Images            []ImageEditUpload `json:"images"`
	Mask              *ImageEditUpload  `json:"mask,omitempty"`
}

const (
	nativeImageHandleTTL      = 10 * time.Minute
	nativeImageHandleMaxBytes = 20 << 20
	nativeImageHandleMaxCount = 8
)

// PickImageFiles invokes the platform-native picker and returns opaque
// handles. The selected path is retained only in the Go process for a short
// period and is revalidated (magic bytes, MIME, dimensions and size) again by
// siteclient immediately before streaming the multipart request.
func (a *App) PickImageFiles(multiple bool) ([]ImageFileHandle, error) {
	if a == nil {
		return nil, errors.New("图片文件选择不可用")
	}
	ctx := a.appContext()
	options := wailsruntime.OpenDialogOptions{
		Title:   "选择参考图",
		Filters: []wailsruntime.FileFilter{{DisplayName: "图片文件", Pattern: "*.png;*.jpg;*.jpeg;*.webp;*.gif"}},
	}
	paths := make([]string, 0, nativeImageHandleMaxCount)
	if multiple {
		selected, err := wailsruntime.OpenMultipleFilesDialog(ctx, options)
		if err != nil {
			return nil, err
		}
		paths = append(paths, selected...)
	} else {
		selected, err := wailsruntime.OpenFileDialog(ctx, options)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(selected) != "" {
			paths = append(paths, selected)
		}
	}
	if len(paths) == 0 {
		return []ImageFileHandle{}, nil
	}
	if len(paths) > nativeImageHandleMaxCount {
		return nil, fmt.Errorf("参考图最多选择 %d 张", nativeImageHandleMaxCount)
	}
	now := time.Now().UTC()
	result := make([]ImageFileHandle, 0, len(paths))
	entries := make(map[string]imageFileHandle, len(paths))
	for _, rawPath := range paths {
		path, err := validatePickedImagePath(rawPath)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("检查参考图: %w", err)
		}
		if info.Size() <= 0 || info.Size() > nativeImageHandleMaxBytes {
			return nil, fmt.Errorf("参考图 %s 超过 %d MiB 限制", filepath.Base(path), nativeImageHandleMaxBytes/(1<<20))
		}
		mimeType := imageMIMEFromPath(path)
		if mimeType == "" {
			return nil, fmt.Errorf("不支持的参考图格式: %s", filepath.Base(path))
		}
		handleID, err := randomRequestID()
		if err != nil {
			return nil, err
		}
		entry := imageFileHandle{path: path, name: filepath.Base(path), contentType: mimeType, bytes: info.Size(), expiresAt: now.Add(nativeImageHandleTTL)}
		entries[handleID] = entry
		result = append(result, ImageFileHandle{ID: handleID, Name: entry.name, ContentType: mimeType, Bytes: entry.bytes, ExpiresAt: entry.expiresAt.Format(time.RFC3339)})
	}
	a.imageHandleMu.Lock()
	if a.imageHandles == nil {
		a.imageHandles = make(map[string]imageFileHandle)
	}
	for id, entry := range entries {
		a.imageHandles[id] = entry
	}
	// Bound stale handles if a user repeatedly opens the picker without
	// submitting a task.
	for id, entry := range a.imageHandles {
		if !entry.expiresAt.After(now) {
			delete(a.imageHandles, id)
		}
	}
	a.imageHandleMu.Unlock()
	return result, nil
}

func validatePickedImagePath(rawPath string) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return "", errors.New("图片路径为空")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil || abs == "." || abs == string(filepath.Separator) {
		return "", errors.New("图片路径无效")
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("图片文件不可读: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("图片必须是普通文件，不接受符号链接或目录")
	}
	return abs, nil
}

func imageMIMEFromPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return ""
	}
}

func (a *App) takeImageFileHandle(handleID string) (imageFileHandle, error) {
	handleID = strings.TrimSpace(handleID)
	if handleID == "" {
		return imageFileHandle{}, errors.New("图片句柄为空")
	}
	now := time.Now().UTC()
	a.imageHandleMu.Lock()
	entry, ok := a.imageHandles[handleID]
	if ok {
		delete(a.imageHandles, handleID)
	}
	a.imageHandleMu.Unlock()
	if !ok || !entry.expiresAt.After(now) {
		return imageFileHandle{}, errors.New("图片句柄已过期，请重新选择文件")
	}
	if _, err := validatePickedImagePath(entry.path); err != nil {
		return imageFileHandle{}, err
	}
	return entry, nil
}

type ImageTaskView struct {
	ID        string                  `json:"id"`
	TaskID    string                  `json:"task_id"`
	Status    string                  `json:"status"`
	PollURL   string                  `json:"poll_url,omitempty"`
	ExpiresAt string                  `json:"expires_at,omitempty"`
	Assets    []siteclient.ImageAsset `json:"assets,omitempty"`
	Error     *siteclient.TaskError   `json:"error,omitempty"`
}

func (a *App) GenerateImage(input ImageGenerateInput) (ImageTaskView, error) {
	// Keep the account identity stable for the entire admission/checkpoint
	// transaction.  In particular, loadClientAndKey may refresh a device
	// session and the task record must be written under the same account/key
	// selection that authorized the request.
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx := a.appContext()
	ownerHash, err := a.currentTaskOwner(ctx)
	if err != nil {
		return ImageTaskView{}, err
	}
	client, key, err := a.loadClientAndKey(ctx)
	if err != nil {
		return ImageTaskView{}, err
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = defaultModel
	}
	task, err := client.GenerateImage(ctx, key, siteclient.ImageGenerateRequest{
		Model: model, Prompt: input.Prompt, N: input.N, Size: input.Size,
		Quality: input.Quality, Background: input.Background,
		OutputFormat: input.OutputFormat, OutputCompression: input.OutputCompression,
	})
	if err != nil {
		return ImageTaskView{}, err
	}
	taskID := task.TaskID
	if taskID == "" {
		taskID = task.ID
	}
	if taskID == "" {
		return ImageTaskView{}, errors.New("server did not return an image task id")
	}
	parameters, marshalErr := json.Marshal(struct {
		N                 int    `json:"n,omitempty"`
		Size              string `json:"size,omitempty"`
		Quality           string `json:"quality,omitempty"`
		Background        string `json:"background,omitempty"`
		OutputFormat      string `json:"output_format,omitempty"`
		OutputCompression int    `json:"output_compression,omitempty"`
	}{input.N, input.Size, input.Quality, input.Background, input.OutputFormat, input.OutputCompression})
	if marshalErr != nil {
		return ImageTaskView{}, fmt.Errorf("serialize image task parameters: %w", marshalErr)
	}
	keyRef := a.activeAPIKeyRef(ctx)
	keyID := a.activeAPIKeyID(ctx)
	if err := a.putTaskForOwner(ctx, ownerHash, imagestore.TaskRecord{
		ID: task.ID, TaskID: taskID, OwnerHash: ownerHash, APIKeyID: keyID, APIKeyRef: keyRef,
		SiteURL: a.siteURL(ctx), GatewayURL: a.gatewayURL(ctx), Status: task.Status, Prompt: input.Prompt,
		Model: model, ParametersJSON: string(parameters),
	}); err != nil {
		return ImageTaskView{}, fmt.Errorf("persist image task checkpoint: %w", err)
	}
	return imageTaskView(task), nil
}

// EditImage submits a multipart image edit using in-memory data URLs for the
// references and optional mask. The resulting async task follows the same
// durable checkpoint path as GenerateImage.
func (a *App) EditImage(input ImageEditInput) (ImageTaskView, error) {
	// Reference files are streamed while the selected account/key is active;
	// prevent a concurrent account switch from changing the credential before
	// the durable task checkpoint is committed.
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx := a.appContext()
	ownerHash, err := a.currentTaskOwner(ctx)
	if err != nil {
		return ImageTaskView{}, err
	}
	client, key, err := a.loadClientAndKey(ctx)
	if err != nil {
		return ImageTaskView{}, err
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = defaultModel
	}
	dataUploads, nativeFiles, err := a.resolveImageEditInputs(input.Images)
	if err != nil {
		return ImageTaskView{}, err
	}
	var dataMask *siteclient.ImageEditUpload
	var nativeMask *siteclient.ImageEditFile
	if input.Mask != nil {
		if strings.TrimSpace(input.Mask.FileHandle) != "" {
			entry, resolveErr := a.takeImageFileHandle(input.Mask.FileHandle)
			if resolveErr != nil {
				return ImageTaskView{}, resolveErr
			}
			nativeMask = &siteclient.ImageEditFile{Name: entry.name, ContentType: entry.contentType, Path: entry.path}
		} else {
			dataMask = imageEditUpload(input.Mask)
		}
	}
	if (len(nativeFiles) > 0 && dataMask != nil) || (len(dataUploads) > 0 && nativeMask != nil) {
		return ImageTaskView{}, errors.New("图片编辑不能混用原生文件和 data URL")
	}
	task, err := client.EditImage(ctx, key, siteclient.ImageEditRequest{
		Model: model, Prompt: input.Prompt, N: input.N, Size: input.Size, Quality: input.Quality,
		Background: input.Background, OutputFormat: input.OutputFormat,
		OutputCompression: input.OutputCompression, InputFidelity: input.InputFidelity,
		Images: dataUploads, Mask: dataMask, Files: nativeFiles, MaskFile: nativeMask,
	})
	if err != nil {
		return ImageTaskView{}, err
	}
	taskID := task.TaskID
	if taskID == "" {
		taskID = task.ID
	}
	if taskID == "" {
		return ImageTaskView{}, errors.New("server did not return an image task id")
	}
	parameters, marshalErr := json.Marshal(struct {
		N                 int    `json:"n,omitempty"`
		Size              string `json:"size,omitempty"`
		Quality           string `json:"quality,omitempty"`
		Background        string `json:"background,omitempty"`
		OutputFormat      string `json:"output_format,omitempty"`
		OutputCompression int    `json:"output_compression,omitempty"`
		InputFidelity     string `json:"input_fidelity,omitempty"`
	}{input.N, input.Size, input.Quality, input.Background, input.OutputFormat, input.OutputCompression, input.InputFidelity})
	if marshalErr != nil {
		return ImageTaskView{}, fmt.Errorf("serialize image edit parameters: %w", marshalErr)
	}
	if err := a.putTaskForOwner(ctx, ownerHash, imagestore.TaskRecord{
		ID: task.ID, TaskID: taskID, OwnerHash: ownerHash, APIKeyID: a.activeAPIKeyID(ctx), APIKeyRef: a.activeAPIKeyRef(ctx), SiteURL: a.siteURL(ctx),
		GatewayURL: a.gatewayURL(ctx), Status: task.Status, Prompt: input.Prompt, Model: model,
		ParametersJSON: string(parameters),
	}); err != nil {
		return ImageTaskView{}, fmt.Errorf("persist image edit checkpoint: %w", err)
	}
	return imageTaskView(task), nil
}

func (a *App) GetImageTask(taskID string) (ImageTaskView, error) {
	return a.getImageTaskWithContext(a.appContext(), taskID)
}

// getImageTaskWithContext is shared by the foreground binding and the startup
// recovery coordinator so a bounded recovery context also bounds profile,
// refresh, and task-poll requests.
func (a *App) getImageTaskWithContext(ctx context.Context, taskID string) (ImageTaskView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Recovery is account-bound work.  Hold a read lock across identity lookup,
	// key ownership revalidation, polling and checkpoint update so logout or a
	// device/API-key switch cannot replace the credential halfway through the
	// request.  The recovery coordinator performs this call independently for
	// each task, so the lock is bounded by the per-task context.
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ownerHash, err := a.currentTaskOwner(ctx)
	if err != nil {
		return ImageTaskView{}, err
	}
	record, err := a.getTaskForOwner(ctx, ownerHash, taskID)
	if err != nil {
		return ImageTaskView{}, err
	}
	if !siteclient.IsOfficialSiteURL(record.SiteURL) || (strings.TrimSpace(record.GatewayURL) != "" && !siteclient.IsOfficialSiteURL(record.GatewayURL)) {
		return ImageTaskView{}, errors.New("拒绝从非官方站点恢复图片任务")
	}
	if record.APIKeyID > 0 && a.config != nil {
		config, configErr := a.config.Load(ctx)
		if configErr != nil {
			return ImageTaskView{}, fmt.Errorf("读取图片任务密钥配置失败: %w", configErr)
		}
		// A zero/missing current ID is also treated as a mismatch. It means the
		// user must explicitly reselect the original key instead of allowing a
		// reset metadata file to silently pair a different secret with this task.
		if config.APIKeyID != record.APIKeyID {
			return ImageTaskView{}, errors.New("创建任务的 API key 已切换，无法安全恢复；请选择原密钥或重新提交")
		}
	}
	keyRef := strings.TrimSpace(record.APIKeyRef)
	if keyRef != apiKeyRef {
		return ImageTaskView{}, errors.New("任务缺少有效的密钥引用，请重新提交任务")
	}
	// Never use the task record's keyring reference as an authority boundary.
	// In device mode loadClientAndKey re-fetches the selected key through the
	// current DPoP-bound account session; in API-key mode it validates the fixed
	// reference and current connection metadata.  This prevents an orphaned
	// keyring value from account A being used after account B is enrolled.
	_, key, err := a.loadClientAndKey(ctx)
	if err != nil {
		return ImageTaskView{}, errors.New("创建任务的 API key 不可用，请重新配置同一把 key")
	}
	client, err := siteclient.New(record.SiteURL, record.GatewayURL)
	if err != nil {
		return ImageTaskView{}, err
	}
	task, err := client.GetImageTask(ctx, key, taskID)
	if err != nil {
		return ImageTaskView{}, err
	}
	_ = a.putTaskForOwner(ctx, ownerHash, imagestore.TaskRecord{
		ID: record.ID, TaskID: taskID, OwnerHash: ownerHash, APIKeyID: record.APIKeyID, APIKeyRef: keyRef,
		SiteURL: record.SiteURL, GatewayURL: record.GatewayURL, Status: task.Status, Prompt: record.Prompt,
		Model: record.Model, ParametersJSON: record.ParametersJSON,
		AssetsDownloaded: record.AssetsDownloaded,
		CreatedAt:        record.CreatedAt,
	})
	return imageTaskView(task), nil
}

// MarkImageTaskAssetsDownloaded records that the result URLs for a completed
// task have all been validated and saved in the private image store. The owner
// check is repeated here so a late renderer callback cannot mark another
// account's task as complete after logout/login.
func (a *App) MarkImageTaskAssetsDownloaded(taskID string) error {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx := a.appContext()
	ownerHash, err := a.currentTaskOwner(ctx)
	if err != nil {
		return err
	}
	return a.markImageTaskAssetsDownloadedWithContext(ctx, ownerHash, taskID)
}

func (a *App) markImageTaskAssetsDownloadedWithContext(ctx context.Context, ownerHash, taskID string) error {
	record, err := a.getTaskForOwner(ctx, ownerHash, taskID)
	if err != nil {
		return err
	}
	if !isSuccessfulImageTaskStatus(record.Status) {
		return errors.New("only a completed image task can be marked downloaded")
	}
	if record.AssetsDownloaded {
		return nil
	}
	record.AssetsDownloaded = true
	return a.putTaskForOwner(ctx, ownerHash, record)
}

type ImageTaskSummary struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	APIKeyID  int64  `json:"api_key_id,omitempty"`
	Status    string `json:"status"`
	Prompt    string `json:"prompt,omitempty"`
	Model     string `json:"model,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func (a *App) ListImageTasks() ([]ImageTaskSummary, error) {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx := a.appContext()
	ownerHash, err := a.currentTaskOwner(ctx)
	if err != nil {
		return nil, err
	}
	records, err := a.listTasksForOwner(ctx, ownerHash)
	if err != nil {
		return nil, err
	}
	result := make([]ImageTaskSummary, 0, len(records))
	for _, record := range records {
		result = append(result, ImageTaskSummary{ID: record.ID, TaskID: record.TaskID, APIKeyID: record.APIKeyID, Status: record.Status, Prompt: record.Prompt, Model: record.Model, CreatedAt: record.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: record.UpdatedAt.UTC().Format(time.RFC3339)})
	}
	return result, nil
}

// ListImageHistory reads server-side async image task history for the account.
// Asset URLs are short-lived and are refreshed by GetImageHistoryAsset rather
// than persisted in the desktop metadata.
func (a *App) ListImageHistory(input ImageHistoryQueryInput) (siteclient.ImageHistoryPage, error) {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx, cancel := context.WithTimeout(a.appContext(), 20*time.Second)
	defer cancel()
	return runSessionRequest(a, ctx, func(client *siteclient.HTTPClient, token string) (siteclient.ImageHistoryPage, error) {
		return client.ListImageHistory(ctx, token, siteclient.ImageHistoryQuery{Cursor: input.Cursor, Status: input.Status, Limit: input.Limit})
	})
}

func (a *App) GetImageHistory(taskID string) (siteclient.ImageHistoryItem, error) {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx, cancel := context.WithTimeout(a.appContext(), 20*time.Second)
	defer cancel()
	return runSessionRequest(a, ctx, func(client *siteclient.HTTPClient, token string) (siteclient.ImageHistoryItem, error) {
		return client.GetImageHistory(ctx, token, taskID)
	})
}

func (a *App) GetImageHistoryAsset(taskID string, index int) (siteclient.ImageHistoryAsset, error) {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx, cancel := context.WithTimeout(a.appContext(), 20*time.Second)
	defer cancel()
	return runSessionRequest(a, ctx, func(client *siteclient.HTTPClient, token string) (siteclient.ImageHistoryAsset, error) {
		return client.GetImageHistoryAsset(ctx, token, taskID, index)
	})
}

// DeleteImageHistory removes a terminal server-side image task for the
// authenticated account. The server performs ownership and object cleanup;
// the desktop client sends only the opaque task ID.
func (a *App) DeleteImageHistory(taskID string) error {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx, cancel := context.WithTimeout(a.appContext(), 20*time.Second)
	defer cancel()
	_, err := runSessionMutation(a, ctx, func(client *siteclient.HTTPClient, token string) (struct{}, error) {
		return struct{}{}, client.DeleteImageHistory(ctx, token, taskID)
	})
	return err
}

// DownloadImage streams a completed (usually signed) URL into the local image
// store. It returns metadata rather than a path to keep the binding serializable.
func (a *App) DownloadImage(sourceURL, name string) (LocalImageAssetSummary, error) {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	sourceURL = strings.TrimSpace(sourceURL)
	if sourceURL == "" {
		return LocalImageAssetSummary{}, errors.New("image URL is required")
	}
	ctx := a.appContext()
	ownerHash, err := a.currentTaskOwner(ctx)
	if err != nil {
		return LocalImageAssetSummary{}, fmt.Errorf("拒绝保存未绑定账户的图片: %w", err)
	}
	store, err := a.imageStoreForOwner()
	if err != nil {
		return LocalImageAssetSummary{}, err
	}
	var (
		asset imagestore.Asset
	)
	if strings.HasPrefix(strings.ToLower(sourceURL), "data:") {
		asset, err = store.SaveDataURLForOwner(ctx, ownerHash, sourceURL, imagestore.AssetMetadata{Name: name})
	} else {
		asset, err = store.DownloadForOwner(ctx, ownerHash, sourceURL, nil, imagestore.AssetMetadata{Name: name})
	}
	if err != nil {
		return LocalImageAssetSummary{}, err
	}
	return localImageAssetSummary(asset), nil
}

func (a *App) ImageLibrary() ([]LocalImageAssetSummary, error) {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx := a.appContext()
	ownerHash, err := a.currentTaskOwner(ctx)
	if err != nil {
		return nil, fmt.Errorf("拒绝读取未绑定账户的图片: %w", err)
	}
	store, err := a.imageStoreForOwner()
	if err != nil {
		return nil, err
	}
	assets, err := store.ListForOwner(ctx, ownerHash)
	if err != nil {
		return nil, err
	}
	result := make([]LocalImageAssetSummary, 0, len(assets))
	for _, asset := range assets {
		result = append(result, localImageAssetSummary(asset))
	}
	return result, nil
}

func (a *App) DeleteImage(id string) error {
	a.accountMu.RLock()
	defer a.accountMu.RUnlock()
	ctx := a.appContext()
	ownerHash, err := a.currentTaskOwner(ctx)
	if err != nil {
		return fmt.Errorf("拒绝删除未绑定账户的图片: %w", err)
	}
	store, err := a.imageStoreForOwner()
	if err != nil {
		return err
	}
	return store.DeleteForOwner(ctx, ownerHash, id)
}

func (a *App) appContext() context.Context {
	a.mu.RLock()
	ctx := a.ctx
	a.mu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// setSessionAccessToken keeps the short-lived bearer in process memory only.
// A small safety window avoids starting a request with a token that is about
// to expire while still honoring the server-provided lifetime.
func (a *App) setSessionAccessToken(token string, expiresIn int) {
	token = strings.TrimSpace(token)
	if expiresIn <= 0 {
		expiresIn = 10 * 60
	}
	expiresAt := time.Now().UTC().Add(time.Duration(expiresIn) * time.Second)
	a.mu.Lock()
	a.sessionAccessToken = token
	a.sessionAccessExpiresAt = expiresAt
	a.mu.Unlock()
}

func (a *App) currentSessionAccessToken() string {
	if a == nil {
		return ""
	}
	a.mu.RLock()
	token, expiresAt := a.sessionAccessToken, a.sessionAccessExpiresAt
	a.mu.RUnlock()
	if strings.TrimSpace(token) == "" || !expiresAt.After(time.Now().UTC().Add(30*time.Second)) {
		return ""
	}
	return token
}

func (a *App) clearSessionAccessToken() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.sessionAccessToken = ""
	a.sessionAccessExpiresAt = time.Time{}
	a.mu.Unlock()
}

// refreshSessionAfterFailure invalidates the exact in-memory token that just
// failed before entering the refresh mutex. This matters when the server has
// revoked a token early: refreshSession deliberately reuses a still-fresh
// in-memory token for ordinary concurrent callers, so a failed request must
// explicitly opt into rotation. If another goroutine already installed a
// newer token, leave it intact and let refreshSession reuse that winner.
func (a *App) refreshSessionAfterFailure(ctx context.Context, client *siteclient.HTTPClient, failedToken string) (string, error) {
	if a != nil {
		a.mu.Lock()
		if strings.TrimSpace(failedToken) == "" || a.sessionAccessToken == strings.TrimSpace(failedToken) {
			a.sessionAccessToken = ""
			a.sessionAccessExpiresAt = time.Time{}
		}
		a.mu.Unlock()
	}
	return a.refreshSession(ctx, client)
}

// runSessionRequest loads a proof-bound desktop session, retries one
// read-only operation after refresh, and persists any rotated DPoP nonce.
// Callers should use the direct methods for non-idempotent mutations such as
// checkout creation.
func runSessionRequest[T any](a *App, ctx context.Context, operation func(*siteclient.HTTPClient, string) (T, error)) (T, error) {
	var zero T
	client, token, err := a.loadClientAndSession(ctx)
	if err != nil {
		return zero, err
	}
	result, err := operation(client, token)
	if err != nil {
		// A failed request must not let refreshSession reuse the rejected
		// in-memory access token. The refresh mutex still serializes concurrent
		// callers, so only one actual token rotation occurs.
		refreshed, refreshErr := a.refreshSessionAfterFailure(ctx, client, token)
		if refreshErr == nil {
			result, err = operation(client, refreshed)
		}
	}
	if err != nil {
		return zero, err
	}
	if nonceErr := a.persistSessionNonce(ctx, client); nonceErr != nil {
		return zero, nonceErr
	}
	return result, nil
}

// runSessionMutation executes a state-changing desktop operation exactly once.
// A failed request may have been accepted by the server before its response was
// lost, so automatic refresh/replay would risk duplicate side effects (most
// notably check-in rewards).  We still persist a rotated DPoP nonce from the
// response and clear the in-memory access token, making the next explicit user
// action refresh the session without replaying this mutation.
func runSessionMutation[T any](a *App, ctx context.Context, operation func(*siteclient.HTTPClient, string) (T, error)) (T, error) {
	var zero T
	client, token, err := a.loadClientAndSession(ctx)
	if err != nil {
		return zero, err
	}
	return runSessionMutationOnce(a, ctx, client, token, operation)
}

func runSessionMutationOnce[T any](a *App, ctx context.Context, client *siteclient.HTTPClient, token string, operation func(*siteclient.HTTPClient, string) (T, error)) (T, error) {
	var zero T
	result, operationErr := operation(client, token)
	nonceErr := a.persistSessionNonce(ctx, client)
	if operationErr != nil {
		a.clearSessionAccessToken()
		if nonceErr != nil {
			return zero, errors.Join(operationErr, nonceErr)
		}
		return zero, operationErr
	}
	if nonceErr != nil {
		return zero, nonceErr
	}
	return result, nil
}

// runSessionRequestNoResult is retained for source compatibility with older
// internal callers. New state-changing operations must use runSessionMutation;
// this helper is intentionally a one-shot wrapper too.
func runSessionRequestNoResult(a *App, ctx context.Context, operation func(*siteclient.HTTPClient, string) error) error {
	client, token, err := a.loadClientAndSession(ctx)
	if err != nil {
		return err
	}
	err = operation(client, token)
	nonceErr := a.persistSessionNonce(ctx, client)
	if err != nil {
		a.clearSessionAccessToken()
		if nonceErr != nil {
			return errors.Join(err, nonceErr)
		}
		return err
	}
	return nonceErr
}

func restoreSecret(ctx context.Context, store securestore.Store, ref, old string, oldErr error) {
	if oldErr == nil {
		_ = store.Set(ctx, ref, old)
	} else {
		_ = store.Delete(ctx, ref)
	}
}

// rollbackAPIKeyConnection restores the exact pre-transition API-key state
// after an atomic config write fails.  The keyring and JSON file are separate
// stores, so restoring one without verifying the other can create a durable
// cross-account pairing.  On any uncertainty, clearLocalStateAfterRevokedSession
// removes all fixed credential references and the metadata file (using a
// detached bounded context when the original request was canceled).
func (a *App) rollbackAPIKeyConnection(ctx context.Context, previous configwriter.ConnectionConfig, oldKey string, oldErr error) error {
	if a == nil || a.secrets == nil || a.config == nil {
		return errors.New("无法验证旧连接状态")
	}
	rollbackCtx, cancel := detachedCleanupContext(ctx)
	defer cancel()

	var restoreErr error
	switch {
	case oldErr == nil:
		if strings.TrimSpace(oldKey) == "" {
			restoreErr = errors.New("旧 API key 为空")
			break
		}
		if err := a.secrets.Set(rollbackCtx, apiKeyRef, oldKey); err != nil {
			restoreErr = fmt.Errorf("恢复旧 API key: %w", err)
			break
		}
		stored, err := a.secrets.Get(rollbackCtx, apiKeyRef)
		if err != nil || stored != oldKey {
			if err == nil {
				err = errors.New("keyring 返回的旧 API key 不匹配")
			}
			restoreErr = fmt.Errorf("验证旧 API key 恢复失败: %w", err)
		}
	case errors.Is(oldErr, securestore.ErrNotFound):
		if err := a.secrets.Delete(rollbackCtx, apiKeyRef); err != nil {
			restoreErr = fmt.Errorf("删除未配置的 API key: %w", err)
			break
		}
		if _, err := a.secrets.Get(rollbackCtx, apiKeyRef); !errors.Is(err, securestore.ErrNotFound) {
			if err == nil {
				err = errors.New("keyring 仍返回 API key")
			}
			restoreErr = fmt.Errorf("验证 API key 清理失败: %w", err)
		}
	default:
		// We could not establish whether a previous secret existed.  Do not
		// guess by deleting or overwriting it; the only safe outcome is a clean
		// local state that requires the user to authenticate/configure again.
		restoreErr = fmt.Errorf("读取旧 API key 失败: %w", oldErr)
	}

	if restoreErr == nil {
		current, loadErr := a.config.Load(rollbackCtx)
		if loadErr != nil {
			restoreErr = fmt.Errorf("验证旧连接配置失败: %w", loadErr)
		} else if !sameConnectionConfig(previous, current) {
			restoreErr = errors.New("连接配置未恢复为切换前状态")
		}
	}
	if restoreErr == nil {
		return nil
	}
	cleanupErr := a.clearLocalStateAfterRevokedSession(rollbackCtx)
	return errors.Join(restoreErr, cleanupErr)
}

// sameConnectionConfig compares the complete metadata record.  The config
// writer's Save contract is atomic; any difference after a failed write means
// that contract cannot be relied upon, so retaining only selected fields would
// risk preserving an untrusted account/nonce combination.
func sameConnectionConfig(expected, actual configwriter.ConnectionConfig) bool {
	return expected == actual
}

// clearStoredAPIKeys removes every API-key secret held by the desktop.  It is
// used at account/session boundaries so a newly authorized account can never
// inherit a key that was selected while another account was active.  The
// operation is fail-closed: callers should abort the transition when a
// credential manager refuses a deletion rather than silently proceeding with
// stale credentials still present.
func clearStoredAPIKeys(ctx context.Context, store securestore.Store) error {
	if store == nil {
		return errors.New("安全凭证存储不可用")
	}
	var cleanupErr error
	for _, ref := range apiKeySecretRefs {
		if err := store.Delete(ctx, ref); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("删除 API key 凭证 %q: %w", ref, err))
		}
	}
	return cleanupErr
}

// clearAPIKeyConfig removes all account-bound key metadata while retaining
// unrelated connection fields such as the official origin and user label.
// A purpose-specific id without its secret is not useful and can cause a
// later account to accidentally resolve an old key, so ids and refs are
// cleared together.
func clearAPIKeyConfig(config *configwriter.ConnectionConfig) {
	if config == nil {
		return
	}
	config.APIKeyRef = ""
	config.APIKeyID = 0
	config.APIKeyHint = ""
	config.CodexAPIKeyID = 0
	config.CodexAPIKeyRef = ""
	config.ClaudeAPIKeyID = 0
	config.ClaudeAPIKeyRef = ""
	config.AccountOwnerHash = ""
}

func isDeviceSessionConfig(config configwriter.ConnectionConfig) bool {
	return strings.EqualFold(strings.TrimSpace(config.AuthMode), "device") || strings.TrimSpace(config.RefreshTokenRef) != ""
}

// isAPIKeyConnectionConfig keeps pre-auth-mode installations working while
// refusing to treat a record carrying refresh metadata as an API-key session.
// The latter distinction is important during interrupted device/API-key
// transitions: stale keyring material must never win over an explicit session
// boundary.
func isAPIKeyConnectionConfig(config configwriter.ConnectionConfig) bool {
	mode := strings.ToLower(strings.TrimSpace(config.AuthMode))
	return mode == "api_key" || (mode == "" && strings.TrimSpace(config.RefreshTokenRef) == "")
}

// isUsableDeviceSessionConfig is the stricter counterpart used before any
// account-bound request.  A stale refresh/DPoP pair may remain in the OS
// keyring when an API-key switch could not delete it; the explicit auth mode
// and fixed references ensure that pair cannot silently resurrect the previous
// account.  The empty-mode case is retained only for early installations that
// already persisted both fixed references but had not yet written AuthMode.
func isUsableDeviceSessionConfig(config configwriter.ConnectionConfig) bool {
	mode := strings.ToLower(strings.TrimSpace(config.AuthMode))
	refsOK := strings.TrimSpace(config.RefreshTokenRef) == refreshTokenRef &&
		strings.TrimSpace(config.DPoPKeyRef) == dpopKeyRef
	if !refsOK {
		return false
	}
	if mode == "device" {
		return true
	}
	return mode == "" && strings.TrimSpace(config.APIKeyRef) == ""
}

// usableAPIKey is deliberately strict at the desktop boundary. A revoked,
// disabled, quota-exhausted, or expired key must never be copied into the
// keyring merely because an eventually-consistent listing still returned it.
func usableAPIKey(key siteclient.APIKey, now time.Time) bool {
	if !strings.EqualFold(strings.TrimSpace(key.Status), "active") {
		return false
	}
	return key.ExpiresAt == nil || key.ExpiresAt.After(now)
}

func apiKeySummary(key siteclient.APIKey) APIKeySummary {
	expiresAt := ""
	if key.ExpiresAt != nil {
		expiresAt = key.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return APIKeySummary{
		ID: key.ID, Name: key.Name, Status: key.Status, KeyHint: securestore.Mask(key.Key),
		Quota: key.Quota, QuotaUsed: key.QuotaUsed, ExpiresAt: expiresAt,
		CurrentConcurrency: key.CurrentConcurrency, Usage5h: key.Usage5h, Usage1d: key.Usage1d, Usage7d: key.Usage7d,
	}
}

func deviceSummary(device siteclient.DeviceInfo) DeviceSummary {
	result := DeviceSummary{
		DeviceID: device.DeviceID, ClientID: device.ClientID, DeviceName: device.DeviceName,
		Scopes: append([]string(nil), device.Scopes...), Audience: device.Audience,
		ProtectionLevel: device.ProtectionLevel, CreatedAt: device.CreatedAt, LastSeenAt: device.LastSeenAt,
	}
	if device.RevokedAt != nil {
		result.RevokedAt = *device.RevokedAt
	}
	return result
}

func localImageAssetSummary(asset imagestore.Asset) LocalImageAssetSummary {
	createdAt := ""
	if !asset.CreatedAt.IsZero() {
		createdAt = asset.CreatedAt.UTC().Format(time.RFC3339)
	}
	return LocalImageAssetSummary{
		ID: asset.ID, Name: asset.Name, MimeType: asset.MimeType, Bytes: asset.Bytes,
		SHA256: asset.SHA256, CreatedAt: createdAt,
	}
}

func (a *App) connectionSummary(ctx context.Context, config configwriter.ConnectionConfig) ConnectionSummary {
	apiRef := "\x00invalid"
	if strings.TrimSpace(config.APIKeyRef) == apiKeyRef {
		apiRef = apiKeyRef
	} else if strings.TrimSpace(config.APIKeyRef) == "" {
		// An empty reference means that no image/gateway key is selected. Do not
		// probe the fixed keyring slot, since it may contain an orphaned value
		// from an older account or an interrupted mode switch.
		apiRef = "\x00missing"
	} else {
		// Treat legacy/modified references as unavailable rather than looking up
		// arbitrary keychain records.
		apiRef = "\x00invalid"
	}
	// Access tokens are no longer persisted. Keep reading the legacy reference
	// only as a cleanup hint elsewhere; session presence is determined by the
	// refresh token that survives an application restart.
	// A partially initialized App can exist during startup, logout, or in a
	// failed platform-store initialization.  Treat an unavailable store as an
	// unavailable credential rather than dereferencing nil or reporting a
	// configured connection from stale metadata.
	secretErr := securestore.ErrNotFound
	if a != nil && a.secrets != nil {
		_, secretErr = a.secrets.Get(ctx, apiRef)
	}
	deviceMode := isUsableDeviceSessionConfig(config)
	sessionConfigured := false
	if deviceMode && a != nil && a.secrets != nil {
		_, sessionErr := a.secrets.Get(ctx, refreshTokenRef)
		sessionConfigured = sessionErr == nil
	}
	// A device connection intentionally does not expose a local API-key
	// credential.  In particular, an orphaned fixed keyring slot from a prior
	// account must not make a device session appear to have an API key selected.
	apiKeyMode := isAPIKeyConnectionConfig(config)
	apiKeyConfigured := secretErr == nil && !deviceMode && apiKeyMode
	siteURL := strings.TrimSpace(config.SiteURL)
	if siteURL == "" {
		siteURL = siteclient.OfficialSiteURL
	}
	authMode := strings.TrimSpace(config.AuthMode)
	if authMode == "" {
		switch {
		case apiKeyConfigured:
			authMode = "api_key"
		case sessionConfigured:
			authMode = "device"
		}
	}
	gatewayURL := strings.TrimSpace(config.GatewayURL)
	if gatewayURL == "" && siteURL != "" {
		gatewayURL = siteURL
	}
	return ConnectionSummary{
		Configured:        apiKeyConfigured || sessionConfigured,
		AuthMode:          authMode,
		SiteURL:           siteURL,
		GatewayURL:        gatewayURL,
		Label:             config.Label,
		APIKeyConfigured:  apiKeyConfigured,
		APIKeyHint:        config.APIKeyHint,
		APIKeyID:          config.APIKeyID,
		CodexAPIKeyID:     config.CodexAPIKeyID,
		ClaudeAPIKeyID:    config.ClaudeAPIKeyID,
		SessionConfigured: sessionConfigured,
		DeviceID:          config.DeviceID,
		ProtectionLevel:   config.ProtectionLevel,
		Scope:             config.Scope,
		UpdatedAt:         config.UpdatedAt.Format(time.RFC3339),
	}
}

func (a *App) loadClientAndKey(ctx context.Context) (*siteclient.HTTPClient, string, error) {
	config, err := a.config.Load(ctx)
	if err != nil {
		return nil, "", err
	}
	if err := validateOfficialConfig(&config); err != nil {
		return nil, "", err
	}
	keyRef := apiKeyRef
	configuredKeyRef := strings.TrimSpace(config.APIKeyRef)
	if configuredKeyRef != "" && configuredKeyRef != apiKeyRef {
		return nil, "", errors.New("API key 引用无效，请重新选择 API key")
	}
	mode := strings.ToLower(strings.TrimSpace(config.AuthMode))
	deviceMode := isUsableDeviceSessionConfig(config)
	if !deviceMode && (mode == "device" || (mode == "" && strings.TrimSpace(config.RefreshTokenRef) != "")) {
		return nil, "", errors.New("设备会话配置无效，请重新授权")
	}
	if deviceMode {
		if config.APIKeyID <= 0 {
			return nil, "", errors.New("请先在当前设备会话下选择用于生图的 API key")
		}
		// The local keyring copy is not an authority boundary. Re-fetch the key
		// through the current DPoP-bound account session and use the returned
		// secret only for this request. This prevents a stale key from account A
		// being used after account B is enrolled on the same machine.
		sessionClient, token, sessionErr := a.loadClientAndSession(ctx)
		if sessionErr != nil {
			return nil, "", sessionErr
		}
		owned, keyErr := sessionClient.GetAPIKey(ctx, token, config.APIKeyID)
		if keyErr != nil {
			return nil, "", errors.New("当前设备会话无法读取所选 API key，请重新授权 api_keys 权限")
		}
		if owned.ID != config.APIKeyID || !usableAPIKey(owned, time.Now().UTC()) || strings.TrimSpace(owned.Key) == "" {
			return nil, "", errors.New("所选 API key 已停用、过期或不属于当前账户")
		}
		gatewayClient, clientErr := siteclient.New(config.SiteURL, config.GatewayURL)
		if clientErr != nil {
			return nil, "", clientErr
		}
		return gatewayClient, strings.TrimSpace(owned.Key), nil
	}
	if configuredKeyRef == "" {
		return nil, "", errors.New("请先配置 API key；密钥不会写入普通配置文件")
	}
	key, err := a.secrets.Get(ctx, keyRef)
	if err != nil {
		return nil, "", errors.New("请先配置 API key；密钥不会写入普通配置文件")
	}
	client, err := siteclient.New(config.SiteURL, config.GatewayURL)
	if err != nil {
		return nil, "", err
	}
	return client, key, nil
}

func (a *App) loadClientAndSession(ctx context.Context) (*siteclient.HTTPClient, string, error) {
	config, err := a.config.Load(ctx)
	if err != nil {
		return nil, "", err
	}
	if err := validateOfficialConfig(&config); err != nil {
		return nil, "", err
	}
	if !isUsableDeviceSessionConfig(config) {
		// Do not fall back to fixed keyring slots just because stale device
		// credentials survived an API-key mode switch.  The connection metadata
		// is the account-mode authority; a device request requires an explicit
		// device mode and both fixed sender-constrained references.
		return nil, "", errors.New("当前连接不是可用的设备会话")
	}
	client, err := siteclient.New(config.SiteURL, config.GatewayURL)
	if err != nil {
		return nil, "", err
	}
	proofRef := dpopKeyRef
	if strings.TrimSpace(config.DPoPKeyRef) != "" && config.DPoPKeyRef != dpopKeyRef {
		return nil, "", errors.New("设备密钥引用无效，请重新授权")
	}
	proofRaw, err := a.secrets.Get(ctx, proofRef)
	if err != nil {
		return nil, "", errors.New("设备密钥不可用，请重新授权")
	}
	proof, err := siteclient.RestorePrivate([]byte(proofRaw))
	if err != nil {
		return nil, "", errors.New("设备密钥损坏，请重新授权")
	}
	client.SetDeviceProof(proof, config.DPoPNonce)
	if token := a.currentSessionAccessToken(); token != "" {
		return client, token, nil
	}
	// The access token is intentionally not persisted. A cold start (or an
	// expired in-memory token) rotates the refresh token once and keeps only the
	// new refresh token/private key in the OS credential store.
	token, refreshErr := a.refreshSession(ctx, client)
	if refreshErr != nil {
		return nil, "", errors.New("请先在官方站点完成设备授权")
	}
	return client, token, nil
}

// restoreDeviceProof reconstructs the sender-constrained key used by logout
// and session requests. The reference is intentionally fixed: connection.json
// is metadata and must not be able to select an arbitrary keyring item. A
// missing nonce is also treated as unusable instead of attempting a proof that
// the server would reject.
func (a *App) restoreDeviceProof(ctx context.Context, config configwriter.ConnectionConfig) (*siteclient.DeviceProof, error) {
	if a == nil || a.secrets == nil {
		return nil, errors.New("设备凭证存储不可用")
	}
	if ref := strings.TrimSpace(config.DPoPKeyRef); ref != "" && ref != dpopKeyRef {
		return nil, errors.New("设备密钥引用无效，请重新授权")
	}
	nonce := strings.TrimSpace(config.DPoPNonce)
	if nonce == "" {
		return nil, errors.New("设备 DPoP nonce 不可用，请重新授权")
	}
	raw, err := a.secrets.Get(ctx, dpopKeyRef)
	if err != nil {
		return nil, errors.New("设备密钥不可用，请重新授权")
	}
	proof, err := siteclient.RestorePrivate([]byte(raw))
	if err != nil {
		return nil, errors.New("设备密钥损坏，请重新授权")
	}
	proof.SetNonce(nonce)
	return proof, nil
}

func (a *App) refreshSession(ctx context.Context, client *siteclient.HTTPClient) (string, error) {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	if token := a.currentSessionAccessToken(); token != "" {
		return token, nil
	}
	// refreshMu only coordinates calls within one process. The GUI and the
	// terminal helper can both observe an expired access token, so serialize the
	// server-side refresh-token rotation across processes as well. Otherwise the
	// second request would be classified as refresh-token reuse and revoke the
	// whole device family.
	releaseProcessLock, lockErr := configwriter.AcquireProcessLock(ctx, filepath.Join(appDataDir(), "device-session-refresh"))
	if lockErr != nil {
		return "", lockErr
	}
	defer func() { _ = releaseProcessLock() }()
	config, err := a.config.Load(ctx)
	if err != nil {
		return "", err
	}
	if err := validateOfficialConfig(&config); err != nil {
		return "", err
	}
	if !isUsableDeviceSessionConfig(config) {
		return "", errors.New("当前连接不是可用的设备会话")
	}
	refreshRef := refreshTokenRef
	proofRef := dpopKeyRef
	if strings.TrimSpace(config.RefreshTokenRef) != "" && config.RefreshTokenRef != refreshTokenRef {
		return "", errors.New("refresh token 引用无效，请重新授权")
	}
	if strings.TrimSpace(config.DPoPKeyRef) != "" && config.DPoPKeyRef != dpopKeyRef {
		return "", errors.New("设备密钥引用无效，请重新授权")
	}
	refresh, err := a.secrets.Get(ctx, refreshRef)
	if err != nil {
		return "", err
	}
	token, err := client.RefreshToken(ctx, refresh)
	if err != nil {
		return "", err
	}
	if err := a.saveDeviceTokensWithRefs(ctx, token, nil, accessTokenRef, refreshRef, proofRef); err != nil {
		// Refresh-token rotation has already consumed the previous server-side
		// token.  If the new credential cannot be committed locally, do not leave
		// the old metadata pointing at a dead session (or a partially written new
		// token).  Revoke the issued family with a detached bounded context and
		// clear all local account material before asking the user to authorize
		// again.
		return "", a.abortRotatedDeviceSession(ctx, client, token, err)
	}
	// The server may rotate the DPoP nonce on every token grant. Keep the
	// current value in metadata so a process restart can restore a proof that
	// the next protected request will accept. The private key remains in the
	// OS keyring and is never written here.
	nonce := strings.TrimSpace(token.DPoPNonce)
	if nonce == "" {
		nonce = strings.TrimSpace(client.DPoPNonce())
	}
	// A refresh proves that this is a desktop session. Persist all resolved
	// references (including defaults for legacy metadata) together with the
	// rotated nonce so a restart can reconstruct the same proof.
	refsChanged := strings.TrimSpace(config.RefreshTokenRef) != refreshRef || strings.TrimSpace(config.DPoPKeyRef) != proofRef || strings.TrimSpace(config.AccessTokenRef) != ""
	modeChanged := config.AuthMode != "device"
	deviceChanged := false
	config.AuthMode = "device"
	config.AccessTokenRef = ""
	config.RefreshTokenRef = refreshRef
	config.DPoPKeyRef = proofRef
	if token.Device != nil {
		deviceChanged = config.DeviceID != token.Device.DeviceID || config.ProtectionLevel != token.Device.ProtectionLevel
		config.DeviceID = token.Device.DeviceID
		config.ProtectionLevel = token.Device.ProtectionLevel
	}
	if strings.TrimSpace(token.Scope) != "" && config.Scope != token.Scope {
		deviceChanged = true
		config.Scope = token.Scope
	}
	nonceChanged := nonce != "" && config.DPoPNonce != nonce
	if nonce != "" {
		config.DPoPNonce = nonce
	}
	if refsChanged || nonceChanged || modeChanged || deviceChanged {
		config.UpdatedAt = time.Now().UTC()
		if saveErr := a.config.Save(ctx, config); saveErr != nil {
			// The access/refresh pair is already rotated at this point.  A canceled
			// UI context (or a transient keyring/filesystem error) should get one
			// short, detached retry before we abandon the session.  If that retry
			// also fails, revoke the new family and remove the stale local metadata
			// rather than returning a session that will fail after restart.
			retryCtx, retryCancel := detachedCleanupContext(ctx)
			retryErr := a.config.Save(retryCtx, config)
			retryCancel()
			if retryErr == nil {
				return token.AccessToken, nil
			}
			return "", a.abortRotatedDeviceSession(ctx, client, token, errors.Join(saveErr, fmt.Errorf("重试保存设备会话元数据失败: %w", retryErr)))
		}
	}
	return token.AccessToken, nil
}

// abortRotatedDeviceSession is the fail-closed boundary after a successful
// refresh rotation whose local commit did not complete.  The server-side
// refresh family is revoked best-effort, but local state is always cleared so
// a stale token/nonce pair can never masquerade as a usable connection.
func (a *App) abortRotatedDeviceSession(ctx context.Context, client *siteclient.HTTPClient, token siteclient.DeviceToken, cause error) error {
	revokeErr := a.revokeIssuedDeviceSession(ctx, client, token.RefreshToken)
	cleanupErr := a.clearLocalStateAfterRevokedSession(ctx)
	return errors.Join(cause, revokeErr, cleanupErr)
}

// persistSessionNonce stores only the current DPoP nonce in connection.json.
// The proof's private key and refresh token stay in securestore. The short-
// lived access token stays in process memory only. This is
// called after every protected response because a server may rotate a nonce
// outside the refresh-token grant as well.
func (a *App) persistSessionNonce(ctx context.Context, client *siteclient.HTTPClient) error {
	if client == nil {
		return nil
	}
	nonce := strings.TrimSpace(client.DPoPNonce())
	if nonce == "" {
		return nil
	}
	config, err := a.config.Load(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(config.RefreshTokenRef) == "" {
		return nil
	}
	if strings.TrimSpace(config.DPoPKeyRef) == "" {
		config.DPoPKeyRef = dpopKeyRef
	}
	if config.DPoPNonce == nonce && config.AuthMode == "device" && strings.TrimSpace(config.DPoPKeyRef) != "" {
		return nil
	}
	config.AuthMode = "device"
	config.DPoPNonce = nonce
	config.UpdatedAt = time.Now().UTC()
	return a.config.Save(ctx, config)
}

func (a *App) siteURL(ctx context.Context) string {
	config, _ := a.config.Load(ctx)
	return config.SiteURL
}

func (a *App) gatewayURL(ctx context.Context) string {
	config, _ := a.config.Load(ctx)
	return config.GatewayURL
}

func (a *App) activeAPIKeyRef(ctx context.Context) string {
	config, _ := a.config.Load(ctx)
	if strings.TrimSpace(config.APIKeyRef) == "" || config.APIKeyRef == apiKeyRef {
		return apiKeyRef
	}
	return apiKeyRef
}

func (a *App) activeAPIKeyID(ctx context.Context) int64 {
	if a == nil || a.config == nil {
		return 0
	}
	config, err := a.config.Load(ctx)
	if err != nil || config.APIKeyID <= 0 {
		return 0
	}
	return config.APIKeyID
}

func fixedOptionalRef(value, expected string) string {
	if strings.TrimSpace(value) == expected {
		return expected
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func validateOfficialConfig(config *configwriter.ConnectionConfig) error {
	if config == nil {
		return errors.New("连接配置不可用")
	}
	if strings.TrimSpace(config.SiteURL) == "" {
		config.SiteURL = siteclient.OfficialSiteURL
	}
	if !siteclient.IsOfficialSiteURL(config.SiteURL) {
		return errors.New("拒绝连接非官方站点")
	}
	if strings.TrimSpace(config.GatewayURL) == "" {
		config.GatewayURL = config.SiteURL
	}
	if !siteclient.IsOfficialSiteURL(config.GatewayURL) {
		return errors.New("拒绝连接非官方 Gateway")
	}
	return nil
}

func imageTaskView(task siteclient.ImageTask) ImageTaskView {
	taskID := task.TaskID
	if taskID == "" {
		taskID = task.ID
	}
	expiresAt := ""
	if task.ExpiresAt > 0 {
		expiresAt = fmt.Sprintf("%d", task.ExpiresAt)
	}
	return ImageTaskView{ID: task.ID, TaskID: taskID, Status: task.Status, PollURL: task.PollURL, ExpiresAt: expiresAt, Assets: task.Assets(), Error: task.Error}
}

func imageEditUploads(values []ImageEditUpload) []siteclient.ImageEditUpload {
	if len(values) == 0 {
		return nil
	}
	result := make([]siteclient.ImageEditUpload, 0, len(values))
	for _, value := range values {
		result = append(result, siteclient.ImageEditUpload{Name: value.Name, ContentType: value.ContentType, DataURL: value.DataURL})
	}
	return result
}

func (a *App) resolveImageEditInputs(values []ImageEditUpload) ([]siteclient.ImageEditUpload, []siteclient.ImageEditFile, error) {
	if len(values) == 0 {
		return nil, nil, nil
	}
	dataUploads := make([]siteclient.ImageEditUpload, 0, len(values))
	nativeFiles := make([]siteclient.ImageEditFile, 0, len(values))
	for index, value := range values {
		handleID := strings.TrimSpace(value.FileHandle)
		if handleID != "" {
			if len(dataUploads) > 0 {
				return nil, nil, errors.New("图片编辑不能混用原生文件和 data URL")
			}
			entry, err := a.takeImageFileHandle(handleID)
			if err != nil {
				return nil, nil, fmt.Errorf("参考图 %d: %w", index+1, err)
			}
			nativeFiles = append(nativeFiles, siteclient.ImageEditFile{Name: entry.name, ContentType: entry.contentType, Path: entry.path})
			continue
		}
		if len(nativeFiles) > 0 {
			return nil, nil, errors.New("图片编辑不能混用原生文件和 data URL")
		}
		if strings.TrimSpace(value.DataURL) == "" {
			return nil, nil, fmt.Errorf("参考图 %d 缺少文件内容", index+1)
		}
		dataUploads = append(dataUploads, siteclient.ImageEditUpload{Name: value.Name, ContentType: value.ContentType, DataURL: value.DataURL})
	}
	return dataUploads, nativeFiles, nil
}

func imageEditUpload(value *ImageEditUpload) *siteclient.ImageEditUpload {
	if value == nil {
		return nil
	}
	return &siteclient.ImageEditUpload{Name: value.Name, ContentType: value.ContentType, DataURL: value.DataURL}
}

func appDataDir() string {
	if value := strings.TrimSpace(os.Getenv("SUB2API_DESKTOP_DATA_DIR")); value != "" {
		return filepath.Clean(value)
	}
	base, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(base) == "" {
		base = "."
	}
	return filepath.Join(base, "Sub2API")
}

// ValidateExternalURL is kept as a small binding-friendly helper for future
// forms; it rejects credentials and non-http(s) schemes before a request is
// attempted.
func ValidateExternalURL(value string) (bool, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false, siteclient.ErrInvalidBaseURL
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.User != nil {
		return false, siteclient.ErrInvalidBaseURL
	}
	return true, nil
}
