// Package siteclient contains the small, native HTTP boundary used by the
// desktop shell. It deliberately does not import the server module so the
// client remains thin and can be released independently.
package siteclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "golang.org/x/image/webp"
)

const (
	defaultTimeout     = 30 * time.Second
	maxResponse        = 4 << 20
	maxClientRedirects = 5
)

var (
	ErrInvalidBaseURL = errors.New("site URL must be an http(s) URL")
	ErrMissingAPIKey  = errors.New("API key is required")
)

type PublicSettings struct {
	SiteName     string `json:"site_name"`
	SiteLogo     string `json:"site_logo"`
	SiteSubtitle string `json:"site_subtitle"`
	APIBaseURL   string `json:"api_base_url"`
}

// ClientCapabilities is the public bootstrap contract exposed by the site.
// Maps are intentional: the server can add feature flags/endpoints without
// forcing an older desktop binary to fail decoding the response.
type ClientCapabilities struct {
	ProtocolVersion string                 `json:"protocol_version"`
	ServerVersion   string                 `json:"server_version,omitempty"`
	ClientID        string                 `json:"client_id"`
	Audience        string                 `json:"audience"`
	APIBaseURL      string                 `json:"api_base_url,omitempty"`
	Scopes          []string               `json:"scopes"`
	DefaultScopes   []string               `json:"default_scopes"`
	HighRiskScopes  []string               `json:"high_risk_scopes"`
	Features        map[string]bool        `json:"features"`
	Availability    map[string]string      `json:"availability"`
	BackendMode     bool                   `json:"backend_mode_enabled"`
	AsyncImages     AsyncImageCapability   `json:"async_images"`
	Endpoints       map[string]string      `json:"endpoints"`
	DeviceFlow      DeviceFlowCapabilities `json:"device_flow"`
}

// IntegrationProfileResponse is the authenticated form of the integration
// profile contract. The server returns only non-secret key metadata and generic
// endpoint configuration; the API key value is never decoded into this DTO.
type IntegrationProfileResponse struct {
	KeySpecific bool                  `json:"key_specific"`
	APIKey      IntegrationProfileKey `json:"api_key"`
	Profiles    []IntegrationProfile  `json:"profiles"`
}

type IntegrationProfileKey struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Available bool   `json:"available"`
}

type IntegrationProfile struct {
	ID               string   `json:"id"`
	ClientID         string   `json:"client_id,omitempty"`
	Audience         string   `json:"audience,omitempty"`
	Auth             string   `json:"auth"`
	GrantType        string   `json:"grant_type,omitempty"`
	RefreshGrantType string   `json:"refresh_grant_type,omitempty"`
	BasePath         string   `json:"base_path"`
	APIKeyID         int64    `json:"api_key_id,omitempty"`
	Available        bool     `json:"available"`
	AsyncCapability  string   `json:"async_capability,omitempty"`
	Endpoints        []string `json:"endpoints,omitempty"`
	Configuration    []string `json:"configuration,omitempty"`
}

// AsyncImageCapability distinguishes admission of new async tasks from
// polling already accepted tasks. The distinction matters when an operator
// disables object storage while clients still need to recover old jobs.
type AsyncImageCapability struct {
	Enabled  bool   `json:"enabled"`
	Pollable bool   `json:"pollable"`
	Reason   string `json:"reason,omitempty"`
}

type DeviceFlowCapabilities struct {
	GrantType        string   `json:"grant_type"`
	ExpiresInSeconds int      `json:"expires_in_seconds"`
	PollInterval     int      `json:"poll_interval_seconds"`
	PKCEMethods      []string `json:"pkce_methods"`
	PublicKeyBinding string   `json:"public_key_binding"`
	TokenType        string   `json:"token_type"`
	DPoPAlgorithms   []string `json:"dpop_algorithms"`
	PublicKeyCurves  []string `json:"public_key_curves"`
	ProofHeader      string   `json:"proof_header"`
	NonceRequired    bool     `json:"nonce_required"`
	AccessTokenHash  string   `json:"access_token_hash"`
}

type ImageCapabilities struct {
	ProtocolVersion string                    `json:"protocol_version"`
	Endpoint        string                    `json:"endpoint"`
	RequiresAPIKey  bool                      `json:"requires_api_key"`
	Operations      []string                  `json:"operations"`
	Models          []ImageModelCapability    `json:"models"`
	Defaults        ImageDefaults             `json:"defaults"`
	Limits          ImageLimits               `json:"limits"`
	Security        ImageSecurityCapabilities `json:"security"`
	Async           AsyncImageCapability      `json:"async"`
	BackendMode     bool                      `json:"backend_mode_enabled"`
	ServerTime      string                    `json:"server_time"`
}

type ImageModelCapability struct {
	ID         string   `json:"id"`
	Operations []string `json:"operations"`
	Enabled    bool     `json:"enabled"`
}

type ImageDefaults struct {
	Model        string `json:"model"`
	N            int    `json:"n"`
	Size         string `json:"size"`
	Quality      string `json:"quality"`
	OutputFormat string `json:"output_format"`
	Background   string `json:"background"`
	PollAfterSec int    `json:"poll_after_seconds"`
}

type ImageLimits struct {
	MaxImages           int   `json:"max_images"`
	MaxReferenceImages  int   `json:"max_reference_images"`
	MaxUploadsWithMask  int   `json:"max_uploads_with_mask"`
	MaxUploadPartBytes  int64 `json:"max_upload_part_bytes"`
	MaxUploadTotalBytes int64 `json:"max_upload_total_bytes"`
	MaxImageDimension   int   `json:"max_image_dimension"`
	MaxImagePixels      int64 `json:"max_image_pixels"`
	MaxDownloadBytes    int64 `json:"max_download_bytes"`
}

type ImageSecurityCapabilities struct {
	AllowedUploadMIMEs  []string `json:"allowed_upload_mimes"`
	MagicBytesRequired  bool     `json:"magic_bytes_required"`
	DecodeDimensions    bool     `json:"decode_dimensions"`
	HTTPSRemoteURLOnly  bool     `json:"https_remote_url_only"`
	PublicRemoteURLOnly bool     `json:"public_remote_url_only"`
	RedirectsValidated  bool     `json:"redirects_validated"`
}

// APIKey is the server representation used internally by the native layer.
// The Key field is never returned from App bindings; SelectAPIKey stores it in
// securestore and exposes only APIKeySummary to the renderer.
type APIKey struct {
	ID                 int64      `json:"id"`
	Key                string     `json:"key"`
	Name               string     `json:"name"`
	Status             string     `json:"status"`
	Quota              float64    `json:"quota"`
	QuotaUsed          float64    `json:"quota_used"`
	ExpiresAt          *time.Time `json:"expires_at"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	CurrentConcurrency int        `json:"current_concurrency"`
	RateLimit5h        float64    `json:"rate_limit_5h"`
	RateLimit1d        float64    `json:"rate_limit_1d"`
	RateLimit7d        float64    `json:"rate_limit_7d"`
	Usage5h            float64    `json:"usage_5h"`
	Usage1d            float64    `json:"usage_1d"`
	Usage7d            float64    `json:"usage_7d"`
}

type APIKeyPage struct {
	Items    []APIKey `json:"items"`
	Total    int64    `json:"total"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
	Pages    int      `json:"pages"`
}

type DeviceList struct {
	Devices []DeviceInfo `json:"devices"`
}

type CheckoutSessionRequest struct {
	Amount                    float64 `json:"amount"`
	PaymentType               string  `json:"payment_type"`
	OrderType                 string  `json:"order_type,omitempty"`
	PlanID                    int64   `json:"plan_id,omitempty"`
	UpgradeFromSubscriptionID int64   `json:"upgrade_from_subscription_id,omitempty"`
	PaymentSource             string  `json:"payment_source,omitempty"`
}

type CheckoutSession struct {
	SessionID                 string    `json:"session_id"`
	Status                    string    `json:"status"`
	OrderID                   int64     `json:"order_id,omitempty"`
	PaymentType               string    `json:"payment_type,omitempty"`
	OrderType                 string    `json:"order_type,omitempty"`
	PlanID                    int64     `json:"plan_id,omitempty"`
	UpgradeFromSubscriptionID int64     `json:"upgrade_from_subscription_id,omitempty"`
	ResultType                string    `json:"result_type,omitempty"`
	Amount                    float64   `json:"amount,omitempty"`
	PayAmount                 float64   `json:"pay_amount,omitempty"`
	Currency                  string    `json:"currency,omitempty"`
	BrowserURL                string    `json:"browser_url,omitempty"`
	ExpiresAt                 time.Time `json:"expires_at"`
	CreatedAt                 time.Time `json:"created_at"`
	PollAfter                 int       `json:"poll_after_seconds"`
	StatusURL                 string    `json:"status_url,omitempty"`
}

type ImageHistoryQuery struct {
	Cursor string
	Status string
	Limit  int
}

type ImageHistoryPage struct {
	Items      []ImageHistoryItem `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
	HasMore    bool               `json:"has_more"`
	ServerTime int64              `json:"server_time"`
}

type ImageHistoryItem struct {
	ID              string          `json:"id"`
	TaskID          string          `json:"task_id"`
	Object          string          `json:"object"`
	Status          string          `json:"status"`
	HTTPStatus      int             `json:"http_status,omitempty"`
	Platform        string          `json:"platform,omitempty"`
	Operation       string          `json:"operation,omitempty"`
	Model           string          `json:"model,omitempty"`
	ImageCount      int             `json:"image_count,omitempty"`
	ResultCount     int             `json:"result_count,omitempty"`
	ResultURLs      []string        `json:"result_urls,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	CreatedAt       int64           `json:"created_at"`
	CompletedAt     *int64          `json:"completed_at,omitempty"`
	ExpiresAt       int64           `json:"expires_at"`
	AssetsAvailable bool            `json:"assets_available"`
	AssetsExpired   bool            `json:"assets_expired,omitempty"`
	Error           any             `json:"error,omitempty"`
}

type ImageHistoryAsset struct {
	TaskID     string `json:"task_id"`
	AssetIndex int    `json:"asset_index"`
	URL        string `json:"url"`
	ExpiresAt  int64  `json:"expires_at"`
}

type UsageSummary struct {
	Mode      string          `json:"mode"`
	IsValid   bool            `json:"isValid"`
	Status    string          `json:"status"`
	PlanName  string          `json:"planName"`
	Remaining float64         `json:"remaining"`
	Balance   float64         `json:"balance"`
	Unit      string          `json:"unit"`
	Quota     json.RawMessage `json:"quota,omitempty"`
	Usage     json.RawMessage `json:"usage,omitempty"`
	Daily     json.RawMessage `json:"daily_usage,omitempty"`
}

// AccountUsageStats mirrors the user dashboard aggregate returned by
// /usage/dashboard/stats.  It intentionally contains only numeric summaries;
// detailed usage logs remain on the web surface and are not copied into the
// desktop process.
type AccountUsageStats struct {
	TotalRequests     int64   `json:"total_requests"`
	TotalTokens       int64   `json:"total_tokens"`
	TotalCost         float64 `json:"total_cost"`
	TotalActualCost   float64 `json:"total_actual_cost"`
	TodayRequests     int64   `json:"today_requests"`
	TodayTokens       int64   `json:"today_tokens"`
	TodayCost         float64 `json:"today_cost"`
	TodayActualCost   float64 `json:"today_actual_cost"`
	AverageDurationMs float64 `json:"average_duration_ms"`
	RPM               int64   `json:"rpm"`
	TPM               int64   `json:"tpm"`
	// Available is true only when the response contains the complete set of
	// aggregate fields consumed by the desktop usage view. It is intentionally
	// excluded from JSON so a missing/null response cannot be mistaken for a
	// valid all-zero snapshot by callers.
	Available bool `json:"-"`
}

var requiredAccountUsageFields = [...]string{
	"total_requests",
	"total_tokens",
	"total_actual_cost",
	"today_requests",
	"today_tokens",
	"today_actual_cost",
}

// UnmarshalJSON records field presence in addition to decoding numeric values.
// The API normally returns all fields, but older/proxied responses may omit
// them or return null; those cases must remain visibly unavailable rather than
// being rendered as zero usage.
func (s *AccountUsageStats) UnmarshalJSON(data []byte) error {
	type accountUsageStats AccountUsageStats
	var decoded accountUsageStats
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*s = AccountUsageStats(decoded)
	s.Available = true
	for _, field := range requiredAccountUsageFields {
		value, ok := fields[field]
		if !ok || string(bytes.TrimSpace(value)) == "null" {
			s.Available = false
			break
		}
	}
	return nil
}

type CheckinResult struct {
	RewardAmount float64 `json:"reward_amount"`
	Balance      float64 `json:"balance"`
	Message      string  `json:"message"`
	CheckedInAt  string  `json:"checked_in_at"`
}

type ImageGenerateRequest struct {
	Model             string `json:"model"`
	Prompt            string `json:"prompt"`
	N                 int    `json:"n,omitempty"`
	Size              string `json:"size,omitempty"`
	Quality           string `json:"quality,omitempty"`
	Background        string `json:"background,omitempty"`
	OutputFormat      string `json:"output_format,omitempty"`
	OutputCompression int    `json:"output_compression,omitempty"`
}

// ImageEditUpload is a browser/Wails-friendly image input. DataURL must be a
// data:image/*;base64 URL; the native client decodes it in memory and sends a
// standards-compliant multipart upload without writing source images to disk.
type ImageEditUpload struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type,omitempty"`
	DataURL     string `json:"data_url"`
}

// ImageEditFile is an internal native-upload descriptor. Path values are
// created only by the Wails file-picker bridge; callers must validate them
// before constructing a request. Keeping this separate from ImageEditUpload
// lets the renderer pass an opaque handle instead of a base64 copy.
type ImageEditFile struct {
	Name        string
	ContentType string
	Path        string
}

type ImageEditRequest struct {
	Model             string            `json:"model"`
	Prompt            string            `json:"prompt"`
	N                 int               `json:"n,omitempty"`
	Size              string            `json:"size,omitempty"`
	Quality           string            `json:"quality,omitempty"`
	Background        string            `json:"background,omitempty"`
	OutputFormat      string            `json:"output_format,omitempty"`
	OutputCompression int               `json:"output_compression,omitempty"`
	InputFidelity     string            `json:"input_fidelity,omitempty"`
	Images            []ImageEditUpload `json:"images"`
	Mask              *ImageEditUpload  `json:"mask,omitempty"`
	Files             []ImageEditFile   `json:"-"`
	MaskFile          *ImageEditFile    `json:"-"`
}

type ImageAsset struct {
	URL           string `json:"url"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

type ImageTask struct {
	ID      string `json:"id"`
	TaskID  string `json:"task_id"`
	Object  string `json:"object,omitempty"`
	Status  string `json:"status"`
	PollURL string `json:"poll_url,omitempty"`
	// The async image service exposes the task lease as Unix seconds. Keep the
	// native transport type numeric so JSON decoding cannot fail when the
	// server returns its canonical representation; the Wails DTO converts it
	// to the display-friendly RFC3339 string at the boundary.
	ExpiresAt int64           `json:"expires_at,omitempty"`
	Error     *TaskError      `json:"error,omitempty"`
	ImageURL  string          `json:"image_url,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Data      []ImageAsset    `json:"data,omitempty"`
}

type CheckinRecord struct {
	Date         string    `json:"date"`
	RewardAmount float64   `json:"reward_amount"`
	CreatedAt    time.Time `json:"created_at"`
}

// CheckinStatus is intentionally a read-only snapshot. It is used to turn a
// duplicate check-in response into an idempotent success without attempting
// the state-changing request a second time.
type CheckinStatus struct {
	Enabled       bool            `json:"enabled"`
	Today         string          `json:"today"`
	CheckedIn     bool            `json:"checked_in"`
	MonthCheckins []CheckinRecord `json:"month_checkins"`
}

type TaskError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type Client interface {
	PublicSettings(ctx context.Context) (PublicSettings, error)
	Usage(ctx context.Context, apiKey string) (UsageSummary, error)
	Checkin(ctx context.Context, accessToken string) (CheckinResult, error)
	GenerateImage(ctx context.Context, apiKey string, request ImageGenerateRequest) (ImageTask, error)
	GetImageTask(ctx context.Context, apiKey, taskID string) (ImageTask, error)
}

// BootstrapClient is an additive capability interface for callers that need
// public feature discovery. It intentionally does not widen Client: existing
// test doubles and embedders implementing the small legacy interface remain
// source-compatible.
type BootstrapClient interface {
	PublicSettings(ctx context.Context) (PublicSettings, error)
	Capabilities(ctx context.Context) (ClientCapabilities, error)
	ImageCapabilities(ctx context.Context) (ImageCapabilities, error)
}

// DesktopAccountClient groups the authenticated account operations used by
// the Wails shell without forcing image-only callers to implement them.
type DesktopAccountClient interface {
	ListAPIKeys(ctx context.Context, accessToken string, page, pageSize int, status string) (APIKeyPage, error)
	Profile(ctx context.Context, accessToken string) (AccountProfile, error)
	Balance(ctx context.Context, accessToken string) (AccountBalance, error)
	AccountUsage(ctx context.Context, accessToken string) (AccountUsageStats, error)
	Checkin(ctx context.Context, accessToken string) (CheckinResult, error)
}

// CheckinStatusClient is separate from DesktopAccountClient so existing
// embedders that only implement the mutation/account summary surface remain
// source-compatible.
type CheckinStatusClient interface {
	CheckinStatus(ctx context.Context, accessToken string) (CheckinStatus, error)
}

// IntegrationProfileClient is kept separate from DesktopAccountClient so
// existing embedders and test doubles do not need to implement the optional
// key-specific integration contract when they only consume account totals.
type IntegrationProfileClient interface {
	IntegrationProfiles(ctx context.Context, accessToken string, apiKeyID int64) (IntegrationProfileResponse, error)
}

var _ BootstrapClient = (*HTTPClient)(nil)
var _ IntegrationProfileClient = (*HTTPClient)(nil)

type HTTPClient struct {
	siteURL     *url.URL
	gatewayURL  *url.URL
	httpClient  *http.Client
	proofMu     sync.RWMutex
	deviceProof *DeviceProof
}

func New(siteURL, gatewayURL string) (*HTTPClient, error) {
	site, err := normalizeBaseURL(siteURL)
	if err != nil {
		return nil, fmt.Errorf("site URL: %w", err)
	}
	gatewayValue := strings.TrimSpace(gatewayURL)
	if gatewayValue == "" {
		gatewayValue = site.String()
	}
	gateway, err := normalizeBaseURL(gatewayValue)
	if err != nil {
		return nil, fmt.Errorf("gateway URL: %w", err)
	}
	client := &HTTPClient{siteURL: site, gatewayURL: gateway}
	client.httpClient = &http.Client{
		Timeout:       defaultTimeout,
		CheckRedirect: client.checkRedirect,
	}
	return client, nil
}

func (c *HTTPClient) PublicSettings(ctx context.Context) (PublicSettings, error) {
	var settings PublicSettings
	if err := c.doJSON(ctx, http.MethodGet, c.siteEndpoint("/settings/public"), "", nil, &settings); err != nil {
		return PublicSettings{}, err
	}
	return settings, nil
}

// Capabilities fetches the public machine-readable client contract. It is
// intentionally unauthenticated so a fresh desktop install can discover
// supported scopes before asking the user to authorize anything.
func (c *HTTPClient) Capabilities(ctx context.Context) (ClientCapabilities, error) {
	var capabilities ClientCapabilities
	if err := c.doJSON(ctx, http.MethodGet, c.siteEndpoint("/client/capabilities"), "", nil, &capabilities); err != nil {
		return ClientCapabilities{}, err
	}
	return capabilities, nil
}

// IntegrationProfiles fetches a key-specific, ownership-checked profile. The
// server requires a proof-bound desktop session (with api_keys scope) or a
// browser JWT; this method never accepts an API key secret as input.
func (c *HTTPClient) IntegrationProfiles(ctx context.Context, accessToken string, apiKeyID int64) (IntegrationProfileResponse, error) {
	if strings.TrimSpace(accessToken) == "" {
		return IntegrationProfileResponse{}, errors.New("account access token is required")
	}
	if apiKeyID <= 0 {
		return IntegrationProfileResponse{}, errors.New("API key id is required")
	}
	values := url.Values{}
	values.Set("api_key_id", strconv.FormatInt(apiKeyID, 10))
	var result IntegrationProfileResponse
	endpoint := c.siteEndpoint("/client/integration-profiles") + "?" + values.Encode()
	if err := c.doJSON(ctx, http.MethodGet, endpoint, accessToken, nil, &result); err != nil {
		return IntegrationProfileResponse{}, err
	}
	return result, nil
}

// ImageCapabilities fetches the credential-free Images API contract from the
// gateway origin. The actual generation/edit routes still require an API key.
func (c *HTTPClient) ImageCapabilities(ctx context.Context) (ImageCapabilities, error) {
	var capabilities ImageCapabilities
	if err := c.doJSON(ctx, http.MethodGet, c.gatewayEndpoint("/images/capabilities"), "", nil, &capabilities); err != nil {
		return ImageCapabilities{}, err
	}
	return capabilities, nil
}

func (c *HTTPClient) Usage(ctx context.Context, apiKey string) (UsageSummary, error) {
	if strings.TrimSpace(apiKey) == "" {
		return UsageSummary{}, ErrMissingAPIKey
	}
	var usage UsageSummary
	if err := c.doJSON(ctx, http.MethodGet, c.gatewayEndpoint("/usage"), apiKey, nil, &usage); err != nil {
		return UsageSummary{}, err
	}
	return usage, nil
}

// AccountUsage fetches the authenticated account aggregate. The returned
// Available flag is set only when the response contains every aggregate field
// consumed by the desktop usage view; missing/null fields remain unavailable.
func (c *HTTPClient) AccountUsage(ctx context.Context, accessToken string) (AccountUsageStats, error) {
	if strings.TrimSpace(accessToken) == "" {
		return AccountUsageStats{}, errors.New("account access token is required")
	}
	var stats AccountUsageStats
	if err := c.doJSON(ctx, http.MethodGet, c.siteEndpoint("/usage/dashboard/stats"), accessToken, nil, &stats); err != nil {
		return AccountUsageStats{}, err
	}
	return stats, nil
}

func (c *HTTPClient) Checkin(ctx context.Context, accessToken string) (CheckinResult, error) {
	if strings.TrimSpace(accessToken) == "" {
		return CheckinResult{}, errors.New("account access token is required")
	}
	var result CheckinResult
	if err := c.doJSON(ctx, http.MethodPost, c.siteEndpoint("/user/checkin"), accessToken, map[string]any{}, &result); err != nil {
		return CheckinResult{}, err
	}
	return result, nil
}

func (c *HTTPClient) CheckinStatus(ctx context.Context, accessToken string) (CheckinStatus, error) {
	if strings.TrimSpace(accessToken) == "" {
		return CheckinStatus{}, errors.New("account access token is required")
	}
	var status CheckinStatus
	if err := c.doJSON(ctx, http.MethodGet, c.siteEndpoint("/user/checkin/status"), accessToken, nil, &status); err != nil {
		return CheckinStatus{}, err
	}
	return status, nil
}

func (c *HTTPClient) ListAPIKeys(ctx context.Context, accessToken string, page, pageSize int, status string) (APIKeyPage, error) {
	if strings.TrimSpace(accessToken) == "" {
		return APIKeyPage{}, errors.New("account access token is required")
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 100
	}
	query := url.Values{}
	query.Set("page", strconv.Itoa(page))
	query.Set("page_size", strconv.Itoa(pageSize))
	if status = strings.TrimSpace(status); status != "" {
		query.Set("status", status)
	}
	var result APIKeyPage
	endpoint := c.siteEndpoint("/keys") + "?" + query.Encode()
	if err := c.doJSON(ctx, http.MethodGet, endpoint, accessToken, nil, &result); err != nil {
		return APIKeyPage{}, err
	}
	return result, nil
}

func (c *HTTPClient) GetAPIKey(ctx context.Context, accessToken string, id int64) (APIKey, error) {
	if strings.TrimSpace(accessToken) == "" {
		return APIKey{}, errors.New("account access token is required")
	}
	if id <= 0 {
		return APIKey{}, errors.New("API key id is required")
	}
	var result APIKey
	path := "/keys/" + strconv.FormatInt(id, 10)
	if err := c.doJSON(ctx, http.MethodGet, c.siteEndpoint(path), accessToken, nil, &result); err != nil {
		return APIKey{}, err
	}
	return result, nil
}

func (c *HTTPClient) ListDevices(ctx context.Context, accessToken string) ([]DeviceInfo, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, errors.New("account access token is required")
	}
	var result DeviceList
	if err := c.doJSON(ctx, http.MethodGet, c.siteEndpoint("/user/devices"), accessToken, nil, &result); err != nil {
		return nil, err
	}
	return append([]DeviceInfo(nil), result.Devices...), nil
}

func (c *HTTPClient) RevokeDevice(ctx context.Context, accessToken, deviceID string) error {
	if strings.TrimSpace(accessToken) == "" {
		return errors.New("account access token is required")
	}
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" || len(deviceID) > 128 {
		return errors.New("device id is required")
	}
	endpoint := c.siteEndpoint("/user/devices/" + url.PathEscape(deviceID))
	return c.doJSON(ctx, http.MethodDelete, endpoint, accessToken, nil, nil)
}

func (c *HTTPClient) CreateCheckoutSession(ctx context.Context, accessToken string, request CheckoutSessionRequest) (CheckoutSession, error) {
	if strings.TrimSpace(accessToken) == "" {
		return CheckoutSession{}, errors.New("account access token is required")
	}
	if request.Amount <= 0 || math.IsNaN(request.Amount) || math.IsInf(request.Amount, 0) {
		return CheckoutSession{}, errors.New("checkout amount must be a positive finite number")
	}
	if strings.TrimSpace(request.PaymentType) == "" {
		return CheckoutSession{}, errors.New("payment type is required")
	}
	var result CheckoutSession
	if err := c.doJSON(ctx, http.MethodPost, c.siteEndpoint("/desktop/checkout-sessions"), accessToken, request, &result); err != nil {
		return CheckoutSession{}, err
	}
	return result, nil
}

func (c *HTTPClient) GetCheckoutSession(ctx context.Context, accessToken, sessionID string) (CheckoutSession, error) {
	if strings.TrimSpace(accessToken) == "" {
		return CheckoutSession{}, errors.New("account access token is required")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(sessionID) > 128 {
		return CheckoutSession{}, errors.New("checkout session id is required")
	}
	var result CheckoutSession
	path := "/desktop/checkout-sessions/" + url.PathEscape(sessionID)
	if err := c.doJSON(ctx, http.MethodGet, c.siteEndpoint(path), accessToken, nil, &result); err != nil {
		return CheckoutSession{}, err
	}
	return result, nil
}

func (c *HTTPClient) ListImageHistory(ctx context.Context, accessToken string, query ImageHistoryQuery) (ImageHistoryPage, error) {
	if strings.TrimSpace(accessToken) == "" {
		return ImageHistoryPage{}, errors.New("account access token is required")
	}
	values := url.Values{}
	if query.Cursor = strings.TrimSpace(query.Cursor); query.Cursor != "" {
		values.Set("cursor", query.Cursor)
	}
	if query.Status = strings.TrimSpace(query.Status); query.Status != "" {
		values.Set("status", query.Status)
	}
	if query.Limit < 1 || query.Limit > 100 {
		query.Limit = 50
	}
	values.Set("limit", strconv.Itoa(query.Limit))
	endpoint := c.siteEndpoint("/user/image-tasks") + "?" + values.Encode()
	var result ImageHistoryPage
	if err := c.doJSON(ctx, http.MethodGet, endpoint, accessToken, nil, &result); err != nil {
		return ImageHistoryPage{}, err
	}
	return result, nil
}

func (c *HTTPClient) GetImageHistory(ctx context.Context, accessToken, taskID string) (ImageHistoryItem, error) {
	if strings.TrimSpace(accessToken) == "" {
		return ImageHistoryItem{}, errors.New("account access token is required")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || len(taskID) > 96 {
		return ImageHistoryItem{}, errors.New("image task id is required")
	}
	var result ImageHistoryItem
	endpoint := c.siteEndpoint("/user/image-tasks/" + url.PathEscape(taskID))
	if err := c.doJSON(ctx, http.MethodGet, endpoint, accessToken, nil, &result); err != nil {
		return ImageHistoryItem{}, err
	}
	return result, nil
}

func (c *HTTPClient) GetImageHistoryAsset(ctx context.Context, accessToken, taskID string, index int) (ImageHistoryAsset, error) {
	if strings.TrimSpace(accessToken) == "" {
		return ImageHistoryAsset{}, errors.New("account access token is required")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || len(taskID) > 96 || index < 0 || index > 99 {
		return ImageHistoryAsset{}, errors.New("image asset reference is invalid")
	}
	var result ImageHistoryAsset
	path := "/user/image-tasks/" + url.PathEscape(taskID) + "/assets/" + strconv.Itoa(index)
	if err := c.doJSON(ctx, http.MethodGet, c.siteEndpoint(path), accessToken, nil, &result); err != nil {
		return ImageHistoryAsset{}, err
	}
	return result, nil
}

// DeleteImageHistory removes one terminal task from the authenticated user's
// server-side history. The server enforces ownership and rejects processing
// tasks; no API key or object key is sent by this client method.
func (c *HTTPClient) DeleteImageHistory(ctx context.Context, accessToken, taskID string) error {
	if strings.TrimSpace(accessToken) == "" {
		return errors.New("account access token is required")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || len(taskID) > 96 {
		return errors.New("image task id is required")
	}
	endpoint := c.siteEndpoint("/user/image-tasks/" + url.PathEscape(taskID))
	return c.doJSON(ctx, http.MethodDelete, endpoint, accessToken, nil, nil)
}

// ResolveOfficialURL resolves a browser URL returned by the official site.
// Relative paths are joined to the pinned origin; absolute URLs must remain
// on that exact HTTPS host so a compromised response cannot redirect a user to
// an unrelated payment page.
func (c *HTTPClient) ResolveOfficialURL(value string) (string, error) {
	if c == nil || c.siteURL == nil {
		return "", ErrInvalidBaseURL
	}
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed == nil || parsed.User != nil {
		return "", ErrInvalidBaseURL
	}
	if parsed.IsAbs() {
		if parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, c.siteURL.Host) {
			return "", ErrInvalidBaseURL
		}
		return parsed.String(), nil
	}
	if parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") {
		return "", ErrInvalidBaseURL
	}
	base := *c.siteURL
	base.Path = parsed.Path
	base.RawPath = parsed.RawPath
	base.RawQuery = parsed.RawQuery
	base.Fragment = parsed.Fragment
	return base.String(), nil
}

func (c *HTTPClient) GenerateImage(ctx context.Context, apiKey string, request ImageGenerateRequest) (ImageTask, error) {
	if strings.TrimSpace(apiKey) == "" {
		return ImageTask{}, ErrMissingAPIKey
	}
	request.Model = strings.TrimSpace(request.Model)
	request.Prompt = strings.TrimSpace(request.Prompt)
	if request.Model == "" || request.Prompt == "" {
		return ImageTask{}, errors.New("image model and prompt are required")
	}
	if request.N <= 0 {
		request.N = 1
	}
	if request.N > 4 {
		request.N = 4
	}
	var task ImageTask
	if err := c.doJSON(ctx, http.MethodPost, c.gatewayEndpoint("/images/generations/async"), apiKey, request, &task); err != nil {
		return ImageTask{}, err
	}
	if task.TaskID == "" {
		task.TaskID = task.ID
	}
	return task, nil
}

const (
	maxImageEditUploadPartBytes int64 = 20 << 20
	maxImageEditUploadBytes     int64 = 80 << 20
	maxImageEditReferences            = 8
)

// EditImage submits an OpenAI-compatible asynchronous image edit using a
// multipart body. Native callers can provide Files so the body is streamed
// from disk; the data-URL form remains for browser/dev compatibility.
func (c *HTTPClient) EditImage(ctx context.Context, apiKey string, request ImageEditRequest) (ImageTask, error) {
	if strings.TrimSpace(apiKey) == "" {
		return ImageTask{}, ErrMissingAPIKey
	}
	request.Model = strings.TrimSpace(request.Model)
	request.Prompt = strings.TrimSpace(request.Prompt)
	if request.Model == "" || request.Prompt == "" {
		return ImageTask{}, errors.New("image edit model and prompt are required")
	}
	if len(request.Images) == 0 && len(request.Files) == 0 {
		return ImageTask{}, errors.New("at least one reference image is required")
	}
	if len(request.Images) > 0 && len(request.Files) > 0 {
		return ImageTask{}, errors.New("image edit cannot mix data URLs and native files")
	}
	imageCount := len(request.Images)
	if len(request.Files) > 0 {
		imageCount = len(request.Files)
	}
	if imageCount > maxImageEditReferences {
		return ImageTask{}, fmt.Errorf("too many reference images (maximum %d)", maxImageEditReferences)
	}
	if request.N <= 0 {
		request.N = 1
	}
	if request.N > 4 {
		request.N = 4
	}

	var task ImageTask
	endpoint := c.gatewayEndpoint("/images/edits/async")
	var requestErr error
	if len(request.Files) > 0 || request.MaskFile != nil {
		requestErr = c.editImageFromFiles(ctx, endpoint, apiKey, request, &task)
	} else {
		requestErr = c.editImageFromDataURLs(ctx, endpoint, apiKey, request, &task)
	}
	if requestErr != nil {
		return ImageTask{}, requestErr
	}
	if task.TaskID == "" {
		task.TaskID = task.ID
	}
	return task, nil
}

func (c *HTTPClient) editImageFromDataURLs(ctx context.Context, endpoint, apiKey string, request ImageEditRequest, target *ImageTask) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writeImageEditFields(writer, request); err != nil {
		return err
	}
	var totalBytes int64
	for index, upload := range request.Images {
		data, contentType, err := decodeImageEditDataURL(upload)
		if err != nil {
			return fmt.Errorf("reference image %d: %w", index+1, err)
		}
		totalBytes += int64(len(data))
		if totalBytes > maxImageEditUploadBytes {
			return fmt.Errorf("image inputs exceed %d bytes in total", maxImageEditUploadBytes)
		}
		if err := writeImageEditPart(writer, imageFieldName(index), upload.Name, contentType, data); err != nil {
			return err
		}
	}
	if request.Mask != nil {
		data, contentType, err := decodeImageEditDataURL(*request.Mask)
		if err != nil {
			return fmt.Errorf("mask image: %w", err)
		}
		totalBytes += int64(len(data))
		if totalBytes > maxImageEditUploadBytes {
			return fmt.Errorf("image inputs exceed %d bytes in total", maxImageEditUploadBytes)
		}
		if err := writeImageEditPart(writer, "mask", request.Mask.Name, contentType, data); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finalize image edit multipart body: %w", err)
	}
	if err := c.doRaw(ctx, http.MethodPost, endpoint, apiKey, writer.FormDataContentType(), &body, target); err != nil {
		return err
	}
	return nil
}

type validatedImageEditFile struct {
	file        ImageEditFile
	contentType string
	size        int64
}

func (c *HTTPClient) editImageFromFiles(ctx context.Context, endpoint, apiKey string, request ImageEditRequest, target *ImageTask) error {
	files := make([]validatedImageEditFile, 0, len(request.Files)+1)
	var totalBytes int64
	for index, file := range request.Files {
		validated, err := validateImageEditFile(file)
		if err != nil {
			return fmt.Errorf("reference image %d: %w", index+1, err)
		}
		totalBytes += validated.size
		if totalBytes > maxImageEditUploadBytes {
			return fmt.Errorf("image inputs exceed %d bytes in total", maxImageEditUploadBytes)
		}
		files = append(files, validated)
	}
	if request.MaskFile != nil {
		validated, err := validateImageEditFile(*request.MaskFile)
		if err != nil {
			return fmt.Errorf("mask image: %w", err)
		}
		totalBytes += validated.size
		if totalBytes > maxImageEditUploadBytes {
			return fmt.Errorf("image inputs exceed %d bytes in total", maxImageEditUploadBytes)
		}
		files = append(files, validated)
	}
	reader, contentType, err := streamImageEditMultipart(ctx, request, files)
	if err != nil {
		return err
	}
	defer reader.Close()
	return c.doRaw(ctx, http.MethodPost, endpoint, apiKey, contentType, reader, target)
}

func writeImageEditFields(writer *multipart.Writer, request ImageEditRequest) error {
	for _, field := range []struct{ name, value string }{
		{"model", request.Model}, {"prompt", request.Prompt},
		{"n", strconv.Itoa(request.N)}, {"size", request.Size}, {"quality", request.Quality},
		{"background", request.Background}, {"output_format", request.OutputFormat},
		{"output_compression", positiveIntString(request.OutputCompression)}, {"input_fidelity", request.InputFidelity},
	} {
		if strings.TrimSpace(field.value) == "" {
			continue
		}
		if err := writer.WriteField(field.name, field.value); err != nil {
			return fmt.Errorf("encode image edit field %s: %w", field.name, err)
		}
	}
	return nil
}

func imageFieldName(index int) string {
	if index == 0 {
		return "image"
	}
	return fmt.Sprintf("image[%d]", index)
}

func validateImageEditFile(file ImageEditFile) (validatedImageEditFile, error) {
	path := strings.TrimSpace(file.Path)
	if path == "" {
		return validatedImageEditFile{}, errors.New("native image path is missing")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return validatedImageEditFile{}, fmt.Errorf("inspect native image: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return validatedImageEditFile{}, errors.New("native image is not a regular file")
	}
	if info.Size() <= 0 || info.Size() > maxImageEditUploadPartBytes {
		return validatedImageEditFile{}, fmt.Errorf("native image exceeds %d bytes", maxImageEditUploadPartBytes)
	}
	opened, err := os.Open(path)
	if err != nil {
		return validatedImageEditFile{}, fmt.Errorf("open native image: %w", err)
	}
	defer opened.Close()
	header := make([]byte, 512)
	n, readErr := io.ReadFull(opened, header)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
		return validatedImageEditFile{}, fmt.Errorf("read native image header: %w", readErr)
	}
	header = header[:n]
	detected := detectImageMIME(header)
	if detected == "" {
		return validatedImageEditFile{}, errors.New("native image bytes are not a supported image")
	}
	declared := strings.TrimSpace(file.ContentType)
	if declared != "" {
		parsed, _, parseErr := mime.ParseMediaType(declared)
		if parseErr != nil || strings.ToLower(strings.TrimSpace(parsed)) != detected {
			return validatedImageEditFile{}, errors.New("native image content type does not match bytes")
		}
	}
	if _, err := opened.Seek(0, io.SeekStart); err != nil {
		return validatedImageEditFile{}, err
	}
	config, format, err := image.DecodeConfig(io.LimitReader(opened, maxImageEditUploadPartBytes))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return validatedImageEditFile{}, errors.New("native image dimensions are invalid")
	}
	if config.Width > 16384 || config.Height > 16384 || int64(config.Width) > 100_000_000/int64(config.Height) {
		return validatedImageEditFile{}, errors.New("native image dimensions exceed safety limits")
	}
	if !decodedImageFormatMatches(format, detected) {
		return validatedImageEditFile{}, errors.New("native image format does not match bytes")
	}
	return validatedImageEditFile{file: file, contentType: detected, size: info.Size()}, nil
}

func streamImageEditMultipart(ctx context.Context, request ImageEditRequest, files []validatedImageEditFile) (*io.PipeReader, string, error) {
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	contentType := multipartWriter.FormDataContentType()
	go func() {
		if err := writeImageEditFields(multipartWriter, request); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		for index, file := range files {
			fieldName := imageFieldName(index)
			if index == len(files)-1 && request.MaskFile != nil {
				fieldName = "mask"
			}
			if err := streamImageEditFile(ctx, multipartWriter, fieldName, file); err != nil {
				_ = writer.CloseWithError(err)
				return
			}
		}
		if err := multipartWriter.Close(); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		_ = writer.Close()
	}()
	return reader, contentType, nil
}

func streamImageEditFile(ctx context.Context, writer *multipart.Writer, fieldName string, file validatedImageEditFile) error {
	opened, err := os.Open(file.file.Path)
	if err != nil {
		return fmt.Errorf("open native image: %w", err)
	}
	defer opened.Close()
	partHeader := make(textproto.MIMEHeader)
	name := strings.TrimSpace(file.file.Name)
	if name == "" {
		name = filepath.Base(file.file.Path)
	}
	name = strings.NewReplacer("/", "_", "\\", "_").Replace(name)
	name = strings.Map(func(r rune) rune {
		if r == '"' || r == '\r' || r == '\n' {
			return '_'
		}
		return r
	}, name)
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fieldName, name))
	partHeader.Set("Content-Type", file.contentType)
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		return err
	}
	written, err := io.Copy(part, io.LimitReader(contextReader{ctx: ctx, reader: opened}, file.size))
	if err != nil {
		return fmt.Errorf("stream native image: %w", err)
	}
	if written != file.size {
		return fmt.Errorf("native image changed while uploading: wrote %d of %d bytes", written, file.size)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(buffer)
	}
}

func decodedImageFormatMatches(format, mimeType string) bool {
	switch mimeType {
	case "image/png":
		return strings.EqualFold(format, "png")
	case "image/jpeg":
		return strings.EqualFold(format, "jpeg")
	case "image/gif":
		return strings.EqualFold(format, "gif")
	case "image/webp":
		return strings.EqualFold(format, "webp")
	default:
		return false
	}
}

func positiveIntString(value int) string {
	if value <= 0 {
		return ""
	}
	return strconv.Itoa(value)
}

func decodeImageEditDataURL(upload ImageEditUpload) ([]byte, string, error) {
	raw := strings.TrimSpace(upload.DataURL)
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return nil, "", errors.New("data_url must be a base64 data URL")
	}
	comma := strings.IndexByte(raw, ',')
	if comma <= len("data:") {
		return nil, "", errors.New("invalid image data URL")
	}
	header := raw[len("data:"):comma]
	parts := strings.Split(header, ";")
	declared := strings.ToLower(strings.TrimSpace(parts[0]))
	if !isSupportedImageMIME(declared) {
		return nil, "", fmt.Errorf("unsupported image MIME type %q", declared)
	}
	base64Part := false
	for _, part := range parts[1:] {
		if strings.EqualFold(strings.TrimSpace(part), "base64") {
			base64Part = true
			break
		}
	}
	if !base64Part {
		return nil, "", errors.New("image data URL must use base64 encoding")
	}
	encoded := strings.TrimSpace(raw[comma+1:])
	if int64(len(encoded)) > (maxImageEditUploadPartBytes/3)*4+8 {
		return nil, "", fmt.Errorf("image exceeds %d bytes", maxImageEditUploadPartBytes)
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) == 0 {
		return nil, "", errors.New("invalid base64 image data")
	}
	if int64(len(data)) > maxImageEditUploadPartBytes {
		return nil, "", fmt.Errorf("image exceeds %d bytes", maxImageEditUploadPartBytes)
	}
	detected := detectImageMIME(data)
	if detected == "" || detected != declared {
		return nil, "", fmt.Errorf("image bytes do not match declared MIME type %q", declared)
	}
	if err := validateImageDimensions(data, detected); err != nil {
		return nil, "", err
	}
	if suppliedRaw := strings.TrimSpace(upload.ContentType); suppliedRaw != "" {
		supplied, _, parseErr := mime.ParseMediaType(suppliedRaw)
		if parseErr != nil {
			return nil, "", fmt.Errorf("invalid content type %q", suppliedRaw)
		}
		if supplied = strings.ToLower(strings.TrimSpace(supplied)); supplied != declared {
			return nil, "", fmt.Errorf("content type %q does not match data URL", supplied)
		}
	}
	return data, declared, nil
}

// validateImageDimensions performs a bounded header decode for both browser
// data-URL inputs and native files.  Magic-byte checks alone are insufficient:
// a tiny crafted PNG/GIF header can advertise a huge pixel count and make the
// upstream decoder allocate an image bomb.
func validateImageDimensions(data []byte, detected string) error {
	config, format, err := image.DecodeConfig(io.LimitReader(bytes.NewReader(data), maxImageEditUploadPartBytes))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return errors.New("image dimensions are invalid")
	}
	if config.Width > 16384 || config.Height > 16384 || int64(config.Width) > 100_000_000/int64(config.Height) {
		return errors.New("image dimensions exceed safety limits")
	}
	if !decodedImageFormatMatches(format, detected) {
		return errors.New("decoded image format does not match bytes")
	}
	return nil
}

func isSupportedImageMIME(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func detectImageMIME(data []byte) string {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png"
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg"
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	default:
		return ""
	}
}

func writeImageEditPart(writer *multipart.Writer, fieldName, fileName, contentType string, data []byte) error {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		fileName = "image"
	}
	fileName = strings.NewReplacer("/", "_", "\\", "_").Replace(fileName)
	fileName = strings.Map(func(r rune) rune {
		switch r {
		case '"', '\r', '\n':
			return '_'
		default:
			return r
		}
	}, fileName)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fieldName, fileName))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return fmt.Errorf("create image edit part %s: %w", fieldName, err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("write image edit part %s: %w", fieldName, err)
	}
	return nil
}

func (c *HTTPClient) GetImageTask(ctx context.Context, apiKey, taskID string) (ImageTask, error) {
	if strings.TrimSpace(apiKey) == "" {
		return ImageTask{}, ErrMissingAPIKey
	}
	if strings.TrimSpace(taskID) == "" {
		return ImageTask{}, errors.New("image task id is required")
	}
	var task ImageTask
	path := "/images/tasks/" + url.PathEscape(strings.TrimSpace(taskID))
	if err := c.doJSON(ctx, http.MethodGet, c.gatewayEndpoint(path), apiKey, nil, &task); err != nil {
		return ImageTask{}, err
	}
	if task.TaskID == "" {
		task.TaskID = task.ID
	}
	return task, nil
}

// Assets extracts OpenAI-style image assets from either the task's direct
// `data` field or the server's raw `result` envelope. The latter is how the
// async task endpoint currently serializes a completed upstream response.
func (t ImageTask) Assets() []ImageAsset {
	if len(t.Data) > 0 {
		if assets := safeImageAssets(t.Data); len(assets) > 0 {
			return assets
		}
	}
	if safeURL, ok := safeImageAssetURL(t.ImageURL); ok {
		return []ImageAsset{{URL: safeURL}}
	}
	if len(t.Result) == 0 {
		return nil
	}
	var envelope struct {
		Data []ImageAsset `json:"data"`
	}
	if json.Unmarshal(t.Result, &envelope) == nil {
		return safeImageAssets(envelope.Data)
	}
	return nil
}

func safeImageAssets(candidates []ImageAsset) []ImageAsset {
	assets := make([]ImageAsset, 0, len(candidates))
	for _, candidate := range candidates {
		safeURL, ok := safeImageAssetURL(candidate.URL)
		if !ok {
			continue
		}
		candidate.URL = safeURL
		assets = append(assets, candidate)
	}
	return assets
}

// safeImageAssetURL keeps renderer/download consumers on ordinary HTTPS URLs.
// Image bytes returned inline as data: URLs must go through the explicit,
// bounded data-URL import path instead of being accepted from a server task.
func safeImageAssetURL(value string) (string, bool) {
	if value == "" || strings.TrimSpace(value) != value || hasControlCharacter(value) {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" {
		return "", false
	}
	for _, component := range []string{parsed.Path, parsed.RawPath, parsed.RawQuery, parsed.Fragment} {
		if hasControlCharacter(component) {
			return "", false
		}
		if decoded, decodeErr := url.PathUnescape(component); decodeErr != nil || hasControlCharacter(decoded) {
			return "", false
		}
	}
	return parsed.String(), true
}

func hasControlCharacter(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

// checkRedirect restricts automatic redirects to the initial configured host
// over HTTPS. This prevents Authorization/API-key/DPoP headers from crossing
// to a different host while still allowing a same-site canonical-path redirect.
func (c *HTTPClient) checkRedirect(request *http.Request, via []*http.Request) error {
	if request == nil || request.URL == nil || len(via) == 0 || via[0] == nil || via[0].URL == nil {
		return errors.New("invalid HTTP redirect")
	}
	if len(via) >= maxClientRedirects {
		return errors.New("too many HTTP redirects")
	}
	target := request.URL
	initial := via[0].URL
	if !strings.EqualFold(initial.Scheme, "https") || !strings.EqualFold(target.Scheme, "https") || target.Host == "" || target.Hostname() == "" || target.User != nil || hasControlCharacter(target.String()) {
		return errors.New("refusing unsafe HTTP redirect")
	}
	if !strings.EqualFold(target.Host, initial.Host) {
		return errors.New("refusing cross-origin HTTP redirect")
	}
	if c == nil || !c.isConfiguredOrigin(initial) {
		return errors.New("refusing redirect from an unconfigured origin")
	}
	request.Header.Del("Referer")
	return nil
}

func (c *HTTPClient) isConfiguredOrigin(value *url.URL) bool {
	if c == nil || value == nil {
		return false
	}
	for _, configured := range []*url.URL{c.siteURL, c.gatewayURL} {
		if configured != nil && strings.EqualFold(value.Scheme, configured.Scheme) && strings.EqualFold(value.Host, configured.Host) {
			return true
		}
	}
	return false
}

func (c *HTTPClient) doJSON(ctx context.Context, method, endpoint, bearer string, body any, target any) error {
	return c.doJSONInternal(ctx, method, endpoint, bearer, "", false, body, target)
}

// doJSONWithDPoP is used for the refresh-token grant, where the request must
// carry a proof but deliberately has no Authorization bearer header. ath is
// left empty for the OAuth token endpoint per RFC 9449.
func (c *HTTPClient) doJSONWithDPoP(ctx context.Context, method, endpoint, bearer string, body any, target any) error {
	return c.doJSONInternal(ctx, method, endpoint, bearer, "", true, body, target)
}

func (c *HTTPClient) doJSONInternal(ctx context.Context, method, endpoint, bearer, athToken string, forceDPoP bool, body any, target any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if (strings.TrimSpace(bearer) != "" || forceDPoP) && !strings.EqualFold(request.URL.Scheme, "https") {
		return errors.New("authenticated requests require HTTPS")
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(bearer) != "" {
		scheme := "Bearer"
		if c.hasDeviceProof() {
			scheme = "DPoP"
		}
		request.Header.Set("Authorization", scheme+" "+strings.TrimSpace(bearer))
	}
	if forceDPoP || strings.TrimSpace(bearer) != "" {
		proofToken := strings.TrimSpace(athToken)
		if proofToken == "" && strings.TrimSpace(bearer) != "" {
			proofToken = strings.TrimSpace(bearer)
		}
		if proof, proofErr := c.dpopProof(method, endpoint, proofToken); proofErr != nil {
			return proofErr
		} else if proof != "" {
			request.Header.Set("DPoP", proof)
		}
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer response.Body.Close()
	c.updateDPoPNonce(response.Header.Get("DPoP-Nonce"))
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if int64(len(data)) > maxResponse {
		return errors.New("server response is too large")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if oauthErr := decodeDeviceOAuthError(response.StatusCode, data); oauthErr != nil {
			return oauthErr
		}
		if apiErr := decodeHTTPError(response.StatusCode, data); apiErr != nil {
			return apiErr
		}
		return fmt.Errorf("server returned HTTP %d: %s", response.StatusCode, responseMessage(data))
	}
	if target == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := decodeEnvelope(data, target); err != nil {
		return fmt.Errorf("decode server response: %w", err)
	}
	return nil
}

// doRaw is the response/error boundary shared by multipart operations. It
// mirrors doJSONInternal's auth, DPoP nonce capture and bounded response
// decoding while allowing callers to provide their own Content-Type/body.
func (c *HTTPClient) doRaw(ctx context.Context, method, endpoint, bearer, contentType string, body io.Reader, target any) error {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if strings.TrimSpace(bearer) != "" && !strings.EqualFold(request.URL.Scheme, "https") {
		return errors.New("authenticated requests require HTTPS")
	}
	request.Header.Set("Accept", "application/json")
	if strings.TrimSpace(contentType) != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if strings.TrimSpace(bearer) != "" {
		scheme := "Bearer"
		if c.hasDeviceProof() {
			scheme = "DPoP"
		}
		request.Header.Set("Authorization", scheme+" "+strings.TrimSpace(bearer))
		if proof, proofErr := c.dpopProof(method, endpoint, bearer); proofErr != nil {
			return proofErr
		} else if proof != "" {
			request.Header.Set("DPoP", proof)
		}
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer response.Body.Close()
	c.updateDPoPNonce(response.Header.Get("DPoP-Nonce"))
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if int64(len(data)) > maxResponse {
		return errors.New("server response is too large")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if oauthErr := decodeDeviceOAuthError(response.StatusCode, data); oauthErr != nil {
			return oauthErr
		}
		if apiErr := decodeHTTPError(response.StatusCode, data); apiErr != nil {
			return apiErr
		}
		return fmt.Errorf("server returned HTTP %d: %s", response.StatusCode, responseMessage(data))
	}
	if target == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := decodeEnvelope(data, target); err != nil {
		return fmt.Errorf("decode server response: %w", err)
	}
	return nil
}

// hasDeviceProof distinguishes a sender-constrained desktop session from an
// ordinary API-key request.  The latter remains Bearer-compatible while the
// former uses the RFC 9449 DPoP authorization scheme in addition to its proof
// header.
func (c *HTTPClient) hasDeviceProof() bool {
	if c == nil {
		return false
	}
	c.proofMu.RLock()
	defer c.proofMu.RUnlock()
	return c.deviceProof != nil
}

// HTTPError preserves the structured reason returned by the standard API
// envelope while retaining the human-readable message used by existing
// callers. Desktop-only idempotency rules (for example duplicate check-in)
// must branch on the stable reason, not on translated prose.
type HTTPError struct {
	StatusCode int
	Reason     string
	Message    string
}

func (e *HTTPError) Error() string {
	if e == nil {
		return "HTTP request failed"
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = "request rejected"
	}
	return fmt.Sprintf("server returned HTTP %d: %s", e.StatusCode, message)
}

func IsHTTPErrorReason(err error, reason string) bool {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(httpErr.Reason), strings.TrimSpace(reason))
}

func decodeHTTPError(status int, data []byte) *HTTPError {
	var payload struct {
		Message string `json:"message"`
		Reason  string `json:"reason"`
		Error   struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &payload)
	reason := strings.TrimSpace(payload.Reason)
	if reason == "" {
		reason = strings.TrimSpace(payload.Error.Code)
	}
	if reason == "" {
		reason = strings.TrimSpace(payload.Error.Type)
	}
	message := strings.TrimSpace(payload.Message)
	if message == "" {
		message = strings.TrimSpace(payload.Error.Message)
	}
	if message == "" {
		message = responseMessage(data)
	}
	return &HTTPError{StatusCode: status, Reason: reason, Message: message}
}

func decodeEnvelope(data []byte, target any) error {
	var envelope struct {
		Code    *int            `json:"code"`
		Message string          `json:"message"`
		Reason  string          `json:"reason"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if envelope.Code != nil {
		if *envelope.Code != 0 {
			if envelope.Message == "" {
				envelope.Message = "request rejected"
			}
			return &HTTPError{StatusCode: *envelope.Code, Reason: strings.TrimSpace(envelope.Reason), Message: envelope.Message}
		}
		if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
			return nil
		}
		return json.Unmarshal(envelope.Data, target)
	}
	return json.Unmarshal(data, target)
}

func responseMessage(data []byte) string {
	var payload struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &payload) == nil {
		if payload.Error.Message != "" {
			return payload.Error.Message
		}
		if payload.Message != "" {
			return payload.Message
		}
	}
	message := strings.TrimSpace(string(data))
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}

func (c *HTTPClient) siteEndpoint(path string) string {
	return endpointWithPrefix(c.siteURL, "/api/v1", path)
}

func (c *HTTPClient) gatewayEndpoint(path string) string {
	return endpointWithPrefix(c.gatewayURL, "/v1", path)
}

func endpointWithPrefix(base *url.URL, prefix, path string) string {
	copyURL := *base
	basePath := strings.TrimRight(copyURL.Path, "/")
	if !strings.HasSuffix(basePath, prefix) {
		basePath += prefix
	}
	copyURL.Path = strings.TrimRight(basePath, "/") + "/" + strings.TrimLeft(path, "/")
	copyURL.RawQuery = ""
	copyURL.Fragment = ""
	return copyURL.String()
}

func normalizeBaseURL(value string) (*url.URL, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, ErrInvalidBaseURL
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, ErrInvalidBaseURL
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, ErrInvalidBaseURL
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("base URL cannot contain credentials, query, or fragment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed, nil
}

// ParseGatewayURL is exported for the UI/native bridge to validate a discovered
// public api_base_url without exposing URL internals.
func ParseGatewayURL(value string) (string, error) {
	parsed, err := normalizeBaseURL(value)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}

// RetryAfterSeconds returns a bounded retry hint for task polling responses.
func RetryAfterSeconds(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds < 1 {
		seconds = 3
	}
	if seconds > 60 {
		seconds = 60
	}
	return time.Duration(seconds) * time.Second
}
