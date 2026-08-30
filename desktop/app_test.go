package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/desktop/internal/configwriter"
	"github.com/Wei-Shaw/sub2api/desktop/internal/imagestore"
	"github.com/Wei-Shaw/sub2api/desktop/internal/securestore"
	"github.com/Wei-Shaw/sub2api/desktop/internal/siteclient"
)

type appTestConfigWriter struct {
	config   configwriter.ConnectionConfig
	loadErr  error
	clearErr error
	cleared  bool
	saves    int
}

func (w *appTestConfigWriter) Load(context.Context) (configwriter.ConnectionConfig, error) {
	if w.loadErr != nil {
		return configwriter.ConnectionConfig{}, w.loadErr
	}
	return w.config, nil
}

func (w *appTestConfigWriter) Save(_ context.Context, config configwriter.ConnectionConfig) error {
	w.saves++
	w.config = config
	return nil
}

func (w *appTestConfigWriter) Clear(context.Context) error {
	w.cleared = true
	return w.clearErr
}

func (w *appTestConfigWriter) Path() string { return "" }

type appFailingSaveWriter struct {
	appTestConfigWriter
	saveErr error
}

func (w *appFailingSaveWriter) Save(_ context.Context, config configwriter.ConnectionConfig) error {
	// Simulate a broken implementation that mutates its in-memory snapshot
	// before reporting an I/O failure. rollbackAPIKeyConnection must detect the
	// mismatch and clear the complete local account state.
	w.saves++
	w.config = config
	return w.saveErr
}

type appFailingRestoreStore struct {
	values       map[string]string
	setCalls     int
	failCallFrom int
}

func (s *appFailingRestoreStore) Set(_ context.Context, name, value string) error {
	s.setCalls++
	if s.failCallFrom > 0 && s.setCalls >= s.failCallFrom {
		return errors.New("simulated keyring restore failure")
	}
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.values[name] = value
	return nil
}

func (s *appFailingRestoreStore) Get(_ context.Context, name string) (string, error) {
	value, ok := s.values[name]
	if !ok {
		return "", securestore.ErrNotFound
	}
	return value, nil
}

func (s *appFailingRestoreStore) Delete(_ context.Context, name string) error {
	delete(s.values, name)
	return nil
}

func TestConnectionSummaryIncludesDeviceProtectionLevel(t *testing.T) {
	ctx := context.Background()
	secrets := securestore.NewMemoryStore()
	if err := secrets.Set(ctx, refreshTokenRef, "refresh-token"); err != nil {
		t.Fatal(err)
	}
	app := &App{secrets: secrets}
	summary := app.connectionSummary(ctx, configwriter.ConnectionConfig{
		SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL, AuthMode: "device",
		RefreshTokenRef: refreshTokenRef, DPoPKeyRef: dpopKeyRef, DeviceID: "device-1", ProtectionLevel: "hardware",
		Scope: "openid profile", UpdatedAt: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
	})
	if !summary.SessionConfigured || summary.DeviceID != "device-1" || summary.ProtectionLevel != "hardware" {
		t.Fatalf("unexpected connection summary: %+v", summary)
	}
}

func TestLocalImageAssetSummaryNeverContainsPath(t *testing.T) {
	asset := localImageAssetSummary(imagestore.Asset{ID: "asset-1", Name: "image.png", MimeType: "image/png", Bytes: 4, Path: "/private/image.png", CreatedAt: time.Now().UTC()})
	encoded, err := json.Marshal(asset)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private/image.png") || strings.Contains(string(encoded), `"path"`) {
		t.Fatalf("local path crossed DTO boundary: %s", encoded)
	}
}

func TestImageLibraryAndDeleteImageAreScopedByCurrentAPIKeyOwner(t *testing.T) {
	ctx := context.Background()
	store, err := imagestore.NewFileStore(filepath.Join(t.TempDir(), "images"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ownerA := imagestore.OwnerHashForSubject("api-key-id:41")
	ownerB := imagestore.OwnerHashForSubject("api-key-id:42")
	assetA, err := store.SaveForOwner(ctx, ownerA, strings.NewReader("owner-a"), imagestore.AssetMetadata{Name: "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	assetB, err := store.SaveForOwner(ctx, ownerB, strings.NewReader("owner-b"), imagestore.AssetMetadata{Name: "b.txt"})
	if err != nil {
		t.Fatal(err)
	}
	secrets := securestore.NewMemoryStore()
	if err := secrets.Set(ctx, apiKeyRef, "owner-a-secret"); err != nil {
		t.Fatal(err)
	}
	app := &App{
		config: &appTestConfigWriter{config: configwriter.ConnectionConfig{
			SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL,
			AuthMode: "api_key", APIKeyRef: apiKeyRef, APIKeyID: 41,
		}},
		secrets: secrets,
		images:  store,
	}
	items, err := app.ImageLibrary()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != assetA.ID {
		t.Fatalf("current account saw cross-owner image assets: %+v", items)
	}
	if err := app.DeleteImage(assetB.ID); !errors.Is(err, imagestore.ErrAssetNotFound) {
		t.Fatalf("cross-owner delete error = %v, want not found", err)
	}
	if _, err := os.Stat(assetB.Path); err != nil {
		t.Fatalf("cross-owner delete removed owner B asset: %v", err)
	}
	if err := app.DeleteImage(assetA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(assetA.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("same-owner delete left owner A asset: %v", err)
	}
}

func TestSaveDeviceConnectionRejectsTamperedGatewayBeforePersisting(t *testing.T) {
	writer := &appTestConfigWriter{config: configwriter.ConnectionConfig{
		SiteURL: siteclient.OfficialSiteURL, GatewayURL: "https://attacker.example", APIKeyRef: apiKeyRef,
	}}
	app := &App{config: writer, secrets: securestore.NewMemoryStore()}
	err := app.saveDeviceConnection(context.Background(), siteclient.DeviceToken{
		AccessToken: "access", RefreshToken: "refresh", DPoPNonce: "nonce",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "Gateway") {
		t.Fatalf("expected tampered gateway rejection, got %v", err)
	}
	if writer.saves != 0 {
		t.Fatal("tampered device config was persisted")
	}
}

func TestSaveDeviceConnectionClearsAPIKeyMaterialAcrossAccountBoundary(t *testing.T) {
	ctx := context.Background()
	writer := &appTestConfigWriter{config: configwriter.ConnectionConfig{
		SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL,
		APIKeyRef: apiKeyRef, APIKeyID: 7, APIKeyHint: "old", CodexAPIKeyRef: codexAPIKeyRef,
		CodexAPIKeyID: 8, ClaudeAPIKeyRef: claudeAPIKeyRef, ClaudeAPIKeyID: 9,
		RefreshTokenRef: refreshTokenRef,
	}}
	secrets := securestore.NewMemoryStore()
	for ref, value := range map[string]string{
		apiKeyRef: "old-image-key", codexAPIKeyRef: "old-codex-key", claudeAPIKeyRef: "old-claude-key",
		refreshTokenRef: "new-refresh",
	} {
		if err := secrets.Set(ctx, ref, value); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{config: writer, secrets: secrets}
	if err := app.saveDeviceConnection(ctx, siteclient.DeviceToken{
		AccessToken: "access", RefreshToken: "new-refresh", DPoPNonce: "nonce",
		Device: &siteclient.DeviceInfo{DeviceID: "device-new", DeviceName: "new", ProtectionLevel: "os"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if writer.config.AuthMode != "device" || writer.config.APIKeyRef != "" || writer.config.APIKeyID != 0 || writer.config.CodexAPIKeyRef != "" || writer.config.ClaudeAPIKeyRef != "" {
		t.Fatalf("device connection retained old key selections: %+v", writer.config)
	}
	for _, ref := range []string{apiKeyRef, codexAPIKeyRef, claudeAPIKeyRef} {
		if _, err := secrets.Get(ctx, ref); !errors.Is(err, securestore.ErrNotFound) {
			t.Fatalf("old keyring entry %q was retained: %v", ref, err)
		}
	}
}

func TestConnectionSummaryDoesNotReportOrphanedKeyringValue(t *testing.T) {
	ctx := context.Background()
	secrets := securestore.NewMemoryStore()
	if err := secrets.Set(ctx, apiKeyRef, "orphaned-key"); err != nil {
		t.Fatal(err)
	}
	app := &App{secrets: secrets}
	summary := app.connectionSummary(ctx, configwriter.ConnectionConfig{
		SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL,
		AuthMode: "device", RefreshTokenRef: refreshTokenRef,
	})
	if summary.APIKeyConfigured || summary.Configured {
		// Configured is expected to remain true only when a refresh token exists;
		// this test intentionally has no refresh token and should therefore report
		// a completely disconnected state despite the orphaned keyring value.
		t.Fatalf("orphaned keyring value affected connection summary: %+v", summary)
	}
}

func TestAPIKeyConnectionModeRecognizesLegacyMetadataOnlyConfig(t *testing.T) {
	tests := []struct {
		name   string
		config configwriter.ConnectionConfig
		want   bool
	}{
		{
			name:   "explicit api key mode",
			config: configwriter.ConnectionConfig{AuthMode: "api_key", RefreshTokenRef: refreshTokenRef},
			want:   true,
		},
		{
			name:   "legacy empty mode without refresh metadata",
			config: configwriter.ConnectionConfig{APIKeyRef: apiKeyRef},
			want:   true,
		},
		{
			name:   "empty mode with refresh metadata",
			config: configwriter.ConnectionConfig{APIKeyRef: apiKeyRef, RefreshTokenRef: refreshTokenRef},
			want:   false,
		},
		{
			name:   "device mode",
			config: configwriter.ConnectionConfig{AuthMode: "device"},
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAPIKeyConnectionConfig(tt.config); got != tt.want {
				t.Fatalf("isAPIKeyConnectionConfig() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIdempotentCheckinResultUsesTodayRecord(t *testing.T) {
	createdAt := time.Date(2026, time.August, 30, 9, 10, 11, 0, time.FixedZone("CST", 8*60*60))
	result := idempotentCheckinResult(siteclient.CheckinStatus{
		Today:     "2026-08-30",
		CheckedIn: true,
		MonthCheckins: []siteclient.CheckinRecord{{
			Date: "2026-08-30", RewardAmount: 1.25, CreatedAt: createdAt,
		}},
	})
	if result.Message != "今日已签到" || result.RewardAmount != 1.25 || result.CheckedInAt != "2026-08-30T01:10:11Z" {
		t.Fatalf("unexpected idempotent result: %+v", result)
	}
}

func TestConnectionSummaryFailsClosedWithoutCredentialStore(t *testing.T) {
	ctx := context.Background()
	app := &App{}
	summary := app.connectionSummary(ctx, configwriter.ConnectionConfig{
		SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL,
		AuthMode: "device", RefreshTokenRef: refreshTokenRef, DPoPKeyRef: dpopKeyRef,
	})
	if summary.Configured || summary.APIKeyConfigured || summary.SessionConfigured {
		t.Fatalf("missing credential store was reported as configured: %+v", summary)
	}
}

func TestConnectionSummaryIgnoresStaleDeviceRefsInAPIKeyMode(t *testing.T) {
	ctx := context.Background()
	secrets := securestore.NewMemoryStore()
	if err := secrets.Set(ctx, refreshTokenRef, "stale-refresh"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Set(ctx, apiKeyRef, "current-api-key"); err != nil {
		t.Fatal(err)
	}
	app := &App{secrets: secrets}
	summary := app.connectionSummary(ctx, configwriter.ConnectionConfig{
		SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL,
		AuthMode: "api_key", APIKeyRef: apiKeyRef, RefreshTokenRef: refreshTokenRef,
		DPoPKeyRef: dpopKeyRef,
	})
	if !summary.APIKeyConfigured || !summary.Configured {
		t.Fatalf("current API-key connection was hidden: %+v", summary)
	}
	if summary.SessionConfigured {
		t.Fatalf("stale device refs were reported as an active session: %+v", summary)
	}
}

func TestConnectionSummaryIgnoresOrphanedAPIKeyInDeviceMode(t *testing.T) {
	ctx := context.Background()
	secrets := securestore.NewMemoryStore()
	for ref, value := range map[string]string{
		apiKeyRef: "old-account-key", refreshTokenRef: "device-refresh",
		dpopKeyRef: "device-proof",
	} {
		if err := secrets.Set(ctx, ref, value); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{secrets: secrets}
	summary := app.connectionSummary(ctx, configwriter.ConnectionConfig{
		SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL,
		AuthMode: "device", APIKeyRef: apiKeyRef, RefreshTokenRef: refreshTokenRef,
		DPoPKeyRef: dpopKeyRef,
	})
	if summary.APIKeyConfigured {
		t.Fatalf("orphaned API key was reported in device mode: %+v", summary)
	}
}

func TestLoadClientAndSessionRejectsStaleDeviceRefsInAPIKeyMode(t *testing.T) {
	ctx := context.Background()
	secrets := securestore.NewMemoryStore()
	if err := secrets.Set(ctx, refreshTokenRef, "stale-refresh"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Set(ctx, dpopKeyRef, "stale-proof"); err != nil {
		t.Fatal(err)
	}
	app := &App{
		config: &appTestConfigWriter{config: configwriter.ConnectionConfig{
			SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL,
			AuthMode: "api_key", APIKeyRef: apiKeyRef,
			RefreshTokenRef: refreshTokenRef, DPoPKeyRef: dpopKeyRef,
		}},
		secrets: secrets,
	}
	if _, _, err := app.loadClientAndSession(ctx); err == nil || !strings.Contains(err.Error(), "不是可用的设备会话") {
		t.Fatalf("stale device refs were used after API-key switch: %v", err)
	}
}

func TestToolAPIKeyDoesNotReadUnboundPurposeSlot(t *testing.T) {
	ctx := context.Background()
	secrets := securestore.NewMemoryStore()
	if err := secrets.Set(ctx, apiKeyRef, "current-account-key"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Set(ctx, codexAPIKeyRef, "old-account-codex-key"); err != nil {
		t.Fatal(err)
	}
	app := &App{secrets: secrets}
	config := configwriter.ConnectionConfig{AuthMode: "api_key", APIKeyRef: apiKeyRef}
	key, err := app.toolAPIKey(ctx, config, string(configwriter.ToolCodex))
	if err != nil {
		t.Fatal(err)
	}
	if key != "current-account-key" {
		t.Fatalf("unbound purpose slot was selected: %q", key)
	}
}

func TestToolAPIKeyDoesNotFallbackFromBoundPurposeSlot(t *testing.T) {
	ctx := context.Background()
	secrets := securestore.NewMemoryStore()
	if err := secrets.Set(ctx, apiKeyRef, "current-account-key"); err != nil {
		t.Fatal(err)
	}
	app := &App{secrets: secrets}
	config := configwriter.ConnectionConfig{
		AuthMode: "api_key", APIKeyRef: apiKeyRef,
		CodexAPIKeyRef: codexAPIKeyRef, CodexAPIKeyID: 11,
	}
	if _, err := app.toolAPIKey(ctx, config, string(configwriter.ToolCodex)); !errors.Is(err, securestore.ErrNotFound) {
		t.Fatalf("missing bound purpose key was unexpectedly replaced by image key: %v", err)
	}
}

func TestGetIntegrationProfilesRejectsInvalidKeyIDBeforeSessionLookup(t *testing.T) {
	app := &App{}
	if _, err := app.GetIntegrationProfiles(0); err == nil || !strings.Contains(err.Error(), "API key ID") {
		t.Fatalf("expected invalid API key ID error, got %v", err)
	}
}

func TestClearConnectionSkipsRemoteLogoutForTamperedOrigin(t *testing.T) {
	writer := &appTestConfigWriter{config: configwriter.ConnectionConfig{
		SiteURL: siteclient.OfficialSiteURL, GatewayURL: "https://attacker.example",
		RefreshTokenRef: "attacker-controlled-ref", APIKeyRef: apiKeyRef,
	}}
	secrets := securestore.NewMemoryStore()
	if err := secrets.Set(context.Background(), refreshTokenRef, "refresh-secret"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Set(context.Background(), "attacker-controlled-ref", "must-remain"); err != nil {
		t.Fatal(err)
	}
	app := &App{config: writer, secrets: secrets}
	if err := app.ClearConnection(); err == nil || !strings.Contains(err.Error(), "官方站点") {
		t.Fatalf("expected explicit remote revocation warning, got %v", err)
	}
	if !writer.cleared {
		t.Fatal("connection metadata was not cleared")
	}
	if _, err := secrets.Get(context.Background(), refreshTokenRef); !errors.Is(err, securestore.ErrNotFound) {
		t.Fatalf("fixed refresh token was not removed: %v", err)
	}
	if value, err := secrets.Get(context.Background(), "attacker-controlled-ref"); err != nil || value != "must-remain" {
		t.Fatalf("tampered keyring entry was touched: %q, %v", value, err)
	}
}

func TestSaveConnectionRevokesOldDeviceSessionBeforeWriting(t *testing.T) {
	ctx := context.Background()
	proof, err := siteclient.NewDeviceProof()
	if err != nil {
		t.Fatal(err)
	}
	proofRaw, err := proof.MarshalPrivate()
	if err != nil {
		t.Fatal(err)
	}
	writer := &appTestConfigWriter{config: configwriter.ConnectionConfig{
		SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL,
		AuthMode: "device", RefreshTokenRef: refreshTokenRef, DPoPKeyRef: dpopKeyRef,
		DPoPNonce: "nonce-old", DeviceID: "device-old",
	}}
	secrets := securestore.NewMemoryStore()
	if err := secrets.Set(ctx, refreshTokenRef, "refresh-old"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Set(ctx, dpopKeyRef, string(proofRaw)); err != nil {
		t.Fatal(err)
	}
	var called int
	var observedRefresh string
	app := &App{
		config:  writer,
		secrets: secrets,
		logoutDeviceSession: func(_ context.Context, client *siteclient.HTTPClient, refresh string) error {
			called++
			observedRefresh = refresh
			if client == nil {
				t.Fatal("revocation client was nil")
			}
			return nil
		},
	}
	if _, err := app.SaveConnection(ConnectionInput{SiteURL: siteclient.OfficialSiteURL, APIKey: "new-api-key"}); err != nil {
		t.Fatalf("save connection failed: %v", err)
	}
	if called != 1 || observedRefresh != "refresh-old" {
		t.Fatalf("old device session was not revoked first: calls=%d refresh=%q", called, observedRefresh)
	}
	if writer.config.AuthMode != "api_key" || writer.config.APIKeyRef != apiKeyRef {
		t.Fatalf("new API-key connection was not persisted: %+v", writer.config)
	}
	if _, err := secrets.Get(ctx, refreshTokenRef); !errors.Is(err, securestore.ErrNotFound) {
		t.Fatalf("old refresh token survived account switch: %v", err)
	}
}

func TestSaveConnectionFailsClosedWhenOldDeviceRevocationFails(t *testing.T) {
	ctx := context.Background()
	proof, err := siteclient.NewDeviceProof()
	if err != nil {
		t.Fatal(err)
	}
	proofRaw, err := proof.MarshalPrivate()
	if err != nil {
		t.Fatal(err)
	}
	original := configwriter.ConnectionConfig{
		SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL,
		AuthMode: "device", RefreshTokenRef: refreshTokenRef, DPoPKeyRef: dpopKeyRef,
		DPoPNonce: "nonce-old", DeviceID: "device-old",
	}
	writer := &appTestConfigWriter{config: original}
	secrets := securestore.NewMemoryStore()
	if err := secrets.Set(ctx, refreshTokenRef, "refresh-old"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Set(ctx, dpopKeyRef, string(proofRaw)); err != nil {
		t.Fatal(err)
	}
	remoteErr := errors.New("network unavailable")
	app := &App{
		config:  writer,
		secrets: secrets,
		logoutDeviceSession: func(context.Context, *siteclient.HTTPClient, string) error {
			return remoteErr
		},
	}
	if _, err := app.SaveConnection(ConnectionInput{SiteURL: siteclient.OfficialSiteURL, APIKey: "new-api-key"}); err == nil || !strings.Contains(err.Error(), "已保留当前连接") {
		t.Fatalf("expected fail-closed transition error, got %v", err)
	}
	if writer.config != original {
		t.Fatalf("failed transition changed connection metadata: %+v", writer.config)
	}
	if _, err := secrets.Get(ctx, apiKeyRef); !errors.Is(err, securestore.ErrNotFound) {
		t.Fatalf("failed transition stored the new API key: %v", err)
	}
	if refresh, err := secrets.Get(ctx, refreshTokenRef); err != nil || refresh != "refresh-old" {
		t.Fatalf("failed transition removed old session: %q, %v", refresh, err)
	}
}

func TestSaveConnectionFailsClosedWhenDeviceRefreshTokenIsMissing(t *testing.T) {
	writer := &appTestConfigWriter{config: configwriter.ConnectionConfig{
		SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL,
		AuthMode: "device", RefreshTokenRef: refreshTokenRef, DPoPKeyRef: dpopKeyRef,
		DPoPNonce: "nonce-old", DeviceID: "device-old",
	}}
	app := &App{config: writer, secrets: securestore.NewMemoryStore()}
	if _, err := app.SaveConnection(ConnectionInput{SiteURL: siteclient.OfficialSiteURL, APIKey: "new-api-key"}); err == nil || !strings.Contains(err.Error(), "已保留当前连接") {
		t.Fatalf("expected missing refresh token to block account switch, got %v", err)
	}
	if writer.config.AuthMode != "device" || writer.config.APIKeyRef != "" {
		t.Fatalf("missing refresh token changed connection metadata: %+v", writer.config)
	}
}

func TestSaveConnectionClearsStateWhenKeyringRollbackCannotBeVerified(t *testing.T) {
	ctx := context.Background()
	previous := configwriter.ConnectionConfig{
		SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL,
		AuthMode: "api_key", APIKeyRef: apiKeyRef, APIKeyHint: securestore.Mask("old-api-key"),
		AccountOwnerHash: imagestore.OwnerHashForSubject("api-key-secret:old-api-key"),
	}
	writer := &appFailingSaveWriter{
		appTestConfigWriter: appTestConfigWriter{config: previous},
		saveErr:             errors.New("simulated config write failure"),
	}
	secrets := &appFailingRestoreStore{
		values:       map[string]string{apiKeyRef: "old-api-key"},
		failCallFrom: 2, // SaveConnection's new write succeeds; rollback Set fails.
	}
	app := &App{config: writer, secrets: secrets}
	_, err := app.SaveConnection(ConnectionInput{SiteURL: siteclient.OfficialSiteURL, APIKey: "new-api-key"})
	if err == nil || !strings.Contains(err.Error(), "无法安全恢复旧连接") {
		t.Fatalf("expected fail-closed rollback error, got %v", err)
	}
	if !writer.cleared {
		t.Fatal("uncertain keyring rollback did not clear connection metadata")
	}
	for _, ref := range []string{apiKeyRef, codexAPIKeyRef, claudeAPIKeyRef, refreshTokenRef, dpopKeyRef} {
		if _, getErr := secrets.Get(ctx, ref); !errors.Is(getErr, securestore.ErrNotFound) {
			t.Fatalf("credential %q survived fail-closed cleanup: %v", ref, getErr)
		}
	}
}

func TestClearConnectionReportsRevocationFailureButCleansLocalState(t *testing.T) {
	ctx := context.Background()
	proof, err := siteclient.NewDeviceProof()
	if err != nil {
		t.Fatal(err)
	}
	proofRaw, err := proof.MarshalPrivate()
	if err != nil {
		t.Fatal(err)
	}
	writer := &appTestConfigWriter{config: configwriter.ConnectionConfig{
		SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL,
		AuthMode: "device", RefreshTokenRef: refreshTokenRef, DPoPKeyRef: dpopKeyRef,
		DPoPNonce: "nonce-old", DeviceID: "device-old",
	}}
	secrets := securestore.NewMemoryStore()
	for ref, value := range map[string]string{refreshTokenRef: "refresh-old", dpopKeyRef: string(proofRaw)} {
		if err := secrets.Set(ctx, ref, value); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{
		config:  writer,
		secrets: secrets,
		logoutDeviceSession: func(context.Context, *siteclient.HTTPClient, string) error {
			return errors.New("remote logout unavailable")
		},
	}
	if err := app.ClearConnection(); err == nil || !strings.Contains(err.Error(), "remote logout unavailable") {
		t.Fatalf("expected explicit revocation error, got %v", err)
	}
	if !writer.cleared {
		t.Fatal("local connection metadata was not cleared")
	}
	for _, ref := range []string{refreshTokenRef, dpopKeyRef} {
		if _, err := secrets.Get(ctx, ref); !errors.Is(err, securestore.ErrNotFound) {
			t.Fatalf("local credential %q survived cleanup: %v", ref, err)
		}
	}
}

func TestRestoreDeviceProofUsesFixedReferenceAndNonce(t *testing.T) {
	ctx := context.Background()
	proof, err := siteclient.NewDeviceProof()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := proof.MarshalPrivate()
	if err != nil {
		t.Fatal(err)
	}
	secrets := securestore.NewMemoryStore()
	if err := secrets.Set(ctx, dpopKeyRef, string(raw)); err != nil {
		t.Fatal(err)
	}
	app := &App{secrets: secrets}
	restored, err := app.restoreDeviceProof(ctx, configwriter.ConnectionConfig{DPoPKeyRef: dpopKeyRef, DPoPNonce: "nonce-1"})
	if err != nil {
		t.Fatal(err)
	}
	if restored == nil || restored.DPoPNonce() != "nonce-1" {
		t.Fatalf("restored proof did not retain server nonce: %#v", restored)
	}
	if _, err := app.restoreDeviceProof(ctx, configwriter.ConnectionConfig{DPoPKeyRef: "attacker-ref", DPoPNonce: "nonce-1"}); err == nil {
		t.Fatal("tampered proof reference was accepted")
	}
	if _, err := app.restoreDeviceProof(ctx, configwriter.ConnectionConfig{DPoPKeyRef: dpopKeyRef}); err == nil {
		t.Fatal("missing DPoP nonce was accepted")
	}
}

func TestUsableAPIKeyRequiresActiveAndUnexpired(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name string
		key  siteclient.APIKey
		want bool
	}{
		{name: "active without expiry", key: siteclient.APIKey{Status: "active"}, want: true},
		{name: "active future expiry", key: siteclient.APIKey{Status: "active", ExpiresAt: ptrTime(now.Add(time.Hour))}, want: true},
		{name: "active expired", key: siteclient.APIKey{Status: "active", ExpiresAt: ptrTime(now.Add(-time.Second))}},
		{name: "disabled", key: siteclient.APIKey{Status: "disabled"}},
		{name: "quota exhausted", key: siteclient.APIKey{Status: "quota_exhausted"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := usableAPIKey(tc.key, now); got != tc.want {
				t.Fatalf("usableAPIKey() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunSessionMutationOnceDoesNotReplayFailedSideEffect(t *testing.T) {
	app := &App{}
	app.setSessionAccessToken("stale-access", 600)
	calls := 0
	_, err := runSessionMutationOnce(app, context.Background(), nil, "stale-access", func(_ *siteclient.HTTPClient, _ string) (struct{}, error) {
		calls++
		return struct{}{}, errors.New("response lost after mutation")
	})
	if err == nil || !strings.Contains(err.Error(), "response lost") {
		t.Fatalf("expected original mutation error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("failed mutation was replayed %d times", calls)
	}
	if token := app.currentSessionAccessToken(); token != "" {
		t.Fatalf("failed mutation should clear the in-memory token, got %q", token)
	}
}

func TestListImageTasksFiltersByCurrentAPIKeyOwner(t *testing.T) {
	ctx := context.Background()
	ownerStore, err := imagestore.NewJSONTaskStore(filepath.Join(t.TempDir(), "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	ownerA := imagestore.OwnerHashForSubject("api-key-id:7")
	ownerB := imagestore.OwnerHashForSubject("api-key-id:8")
	if err := ownerStore.PutForOwner(ctx, ownerA, imagestore.TaskRecord{TaskID: "task-a", ID: "task-a", Status: "processing"}); err != nil {
		t.Fatal(err)
	}
	if err := ownerStore.PutForOwner(ctx, ownerB, imagestore.TaskRecord{TaskID: "task-b", ID: "task-b", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	secrets := securestore.NewMemoryStore()
	if err := secrets.Set(ctx, apiKeyRef, "sk-test-owner-a"); err != nil {
		t.Fatal(err)
	}
	app := &App{
		config:  &appTestConfigWriter{config: configwriter.ConnectionConfig{SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL, AuthMode: "api_key", APIKeyRef: apiKeyRef, APIKeyID: 7}},
		secrets: secrets,
		tasks:   ownerStore,
	}
	items, err := app.ListImageTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].TaskID != "task-a" {
		t.Fatalf("current account saw cross-owner tasks: %+v", items)
	}
}

func TestRecoverOneImageTaskRejectsOwnerChangeBeforePolling(t *testing.T) {
	ctx := context.Background()
	store, err := imagestore.NewJSONTaskStore(filepath.Join(t.TempDir(), "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	secrets := securestore.NewMemoryStore()
	if err := secrets.Set(ctx, apiKeyRef, "owner-a-secret"); err != nil {
		t.Fatal(err)
	}
	ownerA := imagestore.OwnerHashForSubject("api-key-secret:owner-a-secret")
	ownerB := imagestore.OwnerHashForSubject("api-key-secret:owner-b-secret")
	record := imagestore.TaskRecord{
		ID: "task-a", TaskID: "task-a", OwnerHash: ownerA, APIKeyRef: apiKeyRef,
		SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL,
		Status: "processing",
	}
	if err := store.PutForOwner(ctx, ownerA, record); err != nil {
		t.Fatal(err)
	}
	app := &App{
		config:  &appTestConfigWriter{config: configwriter.ConnectionConfig{SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL, AuthMode: "api_key", APIKeyRef: apiKeyRef}},
		secrets: secrets,
		tasks:   store,
	}
	if err := app.recoverOneImageTask(ctx, ownerB, record); err == nil || !strings.Contains(err.Error(), "owner changed") {
		t.Fatalf("expected recovery to stop before polling after owner change, got %v", err)
	}
}

func TestCurrentTaskOwnerRequiresExplicitAPIKeyReference(t *testing.T) {
	ctx := context.Background()
	secrets := securestore.NewMemoryStore()
	if err := secrets.Set(ctx, apiKeyRef, "orphaned-secret"); err != nil {
		t.Fatal(err)
	}
	app := &App{
		config: &appTestConfigWriter{config: configwriter.ConnectionConfig{
			SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL,
			AuthMode: "api_key", APIKeyID: 41,
		}},
		secrets: secrets,
	}
	if _, err := app.currentTaskOwner(ctx); err == nil || !strings.Contains(err.Error(), "API key 引用无效") {
		t.Fatalf("orphaned keyring secret was accepted without an explicit reference: %v", err)
	}
}

func TestGetImageTaskRejectsSelectedKeyChangeBeforeReadingSecret(t *testing.T) {
	ctx := context.Background()
	store, err := imagestore.NewJSONTaskStore(filepath.Join(t.TempDir(), "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	secrets := securestore.NewMemoryStore()
	if err := secrets.Set(ctx, apiKeyRef, "current-secret"); err != nil {
		t.Fatal(err)
	}
	owner := imagestore.OwnerHashForSubject("api-key-id:8")
	if err := store.PutForOwner(ctx, owner, imagestore.TaskRecord{
		ID: "task-a", TaskID: "task-a", OwnerHash: owner, APIKeyID: 7, APIKeyRef: apiKeyRef,
		SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL,
		Status: "processing",
	}); err != nil {
		t.Fatal(err)
	}
	app := &App{
		config: &appTestConfigWriter{config: configwriter.ConnectionConfig{
			SiteURL: siteclient.OfficialSiteURL, GatewayURL: siteclient.OfficialSiteURL,
			AuthMode: "api_key", APIKeyRef: apiKeyRef, APIKeyID: 8,
		}},
		secrets: secrets,
		tasks:   store,
	}
	_, err = app.getImageTaskWithContext(ctx, "task-a")
	if err == nil || !strings.Contains(err.Error(), "API key 已切换") {
		t.Fatalf("expected selected-key mismatch before polling, got %v", err)
	}
}

func TestApplyAccountUsageStatsRequiresCompleteResponse(t *testing.T) {
	summary := UsageSummary{}
	applyAccountUsageStats(&summary, siteclient.AccountUsageStats{TotalRequests: 9})
	if summary.StatsAvailable {
		t.Fatal("incomplete usage stats must remain unavailable")
	}

	applyAccountUsageStats(&summary, siteclient.AccountUsageStats{
		Available:       true,
		TotalRequests:   9,
		TotalTokens:     12,
		TotalActualCost: 0.25,
		TodayRequests:   2,
		TodayTokens:     3,
		TodayActualCost: 0.05,
	})
	if !summary.StatsAvailable || summary.TotalRequests != 9 || summary.TotalActualCost != 0.25 {
		t.Fatalf("complete usage stats not applied: %+v", summary)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
