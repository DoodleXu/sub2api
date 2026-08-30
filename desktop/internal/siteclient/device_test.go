package siteclient

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestDeviceProofRoundTripAndDPoPClaims(t *testing.T) {
	proof, err := NewDeviceProof()
	if err != nil {
		t.Fatal(err)
	}
	proof.SetNonce("nonce-1")
	const accessToken = "access-token"
	const target = "https://ai.clol.site/api/v1/user/profile"
	raw, err := proof.signDPoP(http.MethodGet, target, accessToken)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("invalid compact proof: %q", raw)
	}
	decode := base64.RawURLEncoding.Strict().DecodeString
	headerBytes, err := decode(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	payloadBytes, err := decode(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	signature, err := decode(parts[2])
	if err != nil || len(signature) != 64 {
		t.Fatalf("invalid JOSE signature: %v len=%d", err, len(signature))
	}
	var header struct {
		Type string          `json:"typ"`
		Alg  string          `json:"alg"`
		JWK  json.RawMessage `json:"jwk"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		t.Fatal(err)
	}
	if header.Type != "dpop+jwt" || header.Alg != "ES256" || !bytes.Equal(header.JWK, proof.publicJWK) {
		t.Fatalf("unexpected header: %+v", header)
	}
	var payload struct {
		HTU   string `json:"htu"`
		HTM   string `json:"htm"`
		IAT   int64  `json:"iat"`
		JTI   string `json:"jti"`
		Nonce string `json:"nonce"`
		ATH   string `json:"ath"`
	}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.HTU != target || payload.HTM != "GET" || payload.IAT <= 0 || payload.JTI == "" || payload.Nonce != "nonce-1" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	ath := sha256.Sum256([]byte(accessToken))
	if payload.ATH != base64.RawURLEncoding.EncodeToString(ath[:]) {
		t.Fatalf("access token hash mismatch: %q", payload.ATH)
	}
	if !verifyJOSESignature(proof, parts[0]+"."+parts[1], signature) {
		t.Fatal("proof signature did not verify")
	}
}

func TestNormalizeScopesDefaultsToLowRiskAndDropsUnknown(t *testing.T) {
	if got := normalizeScopes(nil); !reflect.DeepEqual(got, []string{"openid", "profile"}) {
		t.Fatalf("unexpected default scopes: %#v", got)
	}
	got := normalizeScopes([]string{"profile billing", "api_keys", "future_admin"})
	if !reflect.DeepEqual(got, []string{"profile", "billing", "api_keys"}) {
		t.Fatalf("unexpected normalized scopes: %#v", got)
	}
}

func TestHTTPClientAddsDPoPAndTracksNonce(t *testing.T) {
	client, err := New(OfficialSiteURL, "")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewDeviceProof()
	if err != nil {
		t.Fatal(err)
	}
	client.SetDeviceProof(proof, "nonce-1")
	var observed *http.Request
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		observed = request
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Dpop-Nonce": []string{"nonce-2"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":1,"email":"a@example.test","balance":2.5}`)),
			Request:    request,
		}, nil
	})}
	profile, err := client.Profile(context.Background(), "access-token")
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != 1 || profile.Balance != 2.5 {
		t.Fatalf("unexpected profile: %+v", profile)
	}
	if observed == nil || observed.Header.Get("Authorization") != "DPoP access-token" || observed.Header.Get("DPoP") == "" {
		t.Fatalf("missing DPoP authorization/proof headers: %#v", observed)
	}
	if got := client.DPoPNonce(); got != "nonce-2" {
		t.Fatalf("nonce was not updated: %q", got)
	}
}

func TestRefreshTokenUsesDPoPWithoutBearer(t *testing.T) {
	client, err := New(OfficialSiteURL, "")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewDeviceProof()
	if err != nil {
		t.Fatal(err)
	}
	client.SetDeviceProof(proof, "nonce-refresh")
	var observed *http.Request
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		observed = request
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "Dpop-Nonce": []string{"nonce-next"}},
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"new-access","refresh_token":"new-refresh","token_type":"DPoP","dpop_nonce":"nonce-next"}`)),
			Request:    request,
		}, nil
	})}
	token, err := client.RefreshToken(context.Background(), "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "new-access" || token.RefreshToken != "new-refresh" {
		t.Fatalf("unexpected token: %+v", token)
	}
	if observed == nil || observed.Header.Get("Authorization") != "" || observed.Header.Get("DPoP") == "" {
		t.Fatalf("refresh must use proof without bearer: %#v", observed)
	}
	var body map[string]any
	data, _ := io.ReadAll(observed.Body)
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	if body["grant_type"] != "refresh_token" || body["refresh_token"] != "old-refresh" || body["client_id"] != DesktopClientID || body["audience"] != DesktopAudience {
		t.Fatalf("unexpected refresh body: %#v", body)
	}
	if _, ok := body["public_key"]; !ok {
		t.Fatalf("refresh body omitted public key: %#v", body)
	}
	if got := client.DPoPNonce(); got != "nonce-next" {
		t.Fatalf("refresh nonce was not tracked: %q", got)
	}
	parts := strings.Split(observed.Header.Get("DPoP"), ".")
	if len(parts) != 3 {
		t.Fatalf("invalid refresh proof: %q", observed.Header.Get("DPoP"))
	}
	payloadBytes, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		HTU string `json:"htu"`
		ATH string `json:"ath"`
	}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.HTU != OfficialSiteURL+"/api/v1/desktop/token" || payload.ATH != "" {
		t.Fatalf("refresh proof must omit ath and bind canonical URL: %+v", payload)
	}
}

func TestRefreshTokenAppliesNonceReturnedOnlyInJSON(t *testing.T) {
	client, err := New(OfficialSiteURL, "")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewDeviceProof()
	if err != nil {
		t.Fatal(err)
	}
	client.SetDeviceProof(proof, "nonce-old")
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"new-access","refresh_token":"new-refresh","dpop_nonce":"nonce-json"}`)),
			Request:    request,
		}, nil
	})}
	if _, err := client.RefreshToken(context.Background(), "old-refresh"); err != nil {
		t.Fatal(err)
	}
	if got := client.DPoPNonce(); got != "nonce-json" {
		t.Fatalf("JSON DPoP nonce was not applied: %q", got)
	}
}

func TestLogoutDeviceRequiresDPoPBeforeSendingRefreshToken(t *testing.T) {
	client, err := New(OfficialSiteURL, "")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})}
	err = client.LogoutDevice(context.Background(), "refresh-secret")
	if err == nil || !strings.Contains(err.Error(), "DPoP") {
		t.Fatalf("expected missing-proof rejection, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("refresh token reached transport without a device proof: %d calls", calls)
	}
}

func TestLogoutDeviceUsesDPoPWithoutBearer(t *testing.T) {
	client, err := New(OfficialSiteURL, "")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewDeviceProof()
	if err != nil {
		t.Fatal(err)
	}
	client.SetDeviceProof(proof, "logout-nonce")
	var observed *http.Request
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		observed = request
		return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})}
	if err := client.LogoutDevice(context.Background(), "refresh-secret"); err != nil {
		t.Fatal(err)
	}
	if observed == nil {
		t.Fatal("logout request was not sent")
	}
	if observed.Method != http.MethodPost || observed.URL.Path != "/api/v1/desktop/logout" {
		t.Fatalf("unexpected logout request: %s %s", observed.Method, observed.URL)
	}
	if observed.Header.Get("Authorization") != "" || observed.Header.Get("DPoP") == "" {
		t.Fatalf("logout must use proof without bearer authorization: %v", observed.Header)
	}
	body, readErr := io.ReadAll(observed.Body)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var payload map[string]string
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["refresh_token"] != "refresh-secret" {
		t.Fatalf("unexpected logout payload: %#v", payload)
	}
	parts := strings.Split(observed.Header.Get("DPoP"), ".")
	if len(parts) != 3 {
		t.Fatalf("invalid logout proof: %q", observed.Header.Get("DPoP"))
	}
	proofPayload, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		HTU string `json:"htu"`
		HTM string `json:"htm"`
		ATH string `json:"ath"`
	}
	if err := json.Unmarshal(proofPayload, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.HTU != OfficialSiteURL+"/api/v1/desktop/logout" || claims.HTM != http.MethodPost || claims.ATH != "" {
		t.Fatalf("unexpected logout proof claims: %+v", claims)
	}
}

func TestDPoPHTUOmitsQueryAndFragment(t *testing.T) {
	proof, err := NewDeviceProof()
	if err != nil {
		t.Fatal(err)
	}
	proof.SetNonce("nonce")
	raw, err := proof.signDPoP(http.MethodGet, OfficialSiteURL+"/api/v1/keys?page=1#ignored", "access")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(raw, ".")
	payloadBytes, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		HTU string `json:"htu"`
	}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.HTU != OfficialSiteURL+"/api/v1/keys" {
		t.Fatalf("unexpected canonical htu: %q", payload.HTU)
	}
}

func verifyJOSESignature(proof *DeviceProof, input string, signature []byte) bool {
	if len(signature) != 64 {
		return false
	}
	hash := sha256.Sum256([]byte(input))
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])
	return ecdsa.Verify(&proof.privateKey.PublicKey, hash[:], r, s)
}
