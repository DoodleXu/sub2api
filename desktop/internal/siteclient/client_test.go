package siteclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageTaskUnmarshalAcceptsNumericExpiresAt(t *testing.T) {
	var task ImageTask
	if err := json.Unmarshal([]byte(`{"id":"task-1","task_id":"task-1","status":"processing","expires_at":1735689600}`), &task); err != nil {
		t.Fatal(err)
	}
	if task.ExpiresAt != 1735689600 {
		t.Fatalf("expires_at = %d", task.ExpiresAt)
	}
}

func TestAccountUsageStatsTracksCompleteFieldPresence(t *testing.T) {
	var complete AccountUsageStats
	if err := json.Unmarshal([]byte(`{"total_requests":1,"total_tokens":2,"total_actual_cost":0.3,"today_requests":4,"today_tokens":5,"today_actual_cost":0.6}`), &complete); err != nil {
		t.Fatal(err)
	}
	if !complete.Available || complete.TotalRequests != 1 || complete.TodayActualCost != 0.6 {
		t.Fatalf("complete stats decoded incorrectly: %+v", complete)
	}

	for _, payload := range []string{
		`{"total_requests":1,"total_tokens":2,"total_actual_cost":0.3}`,
		`{"total_requests":null,"total_tokens":2,"total_actual_cost":0.3,"today_requests":4,"today_tokens":5,"today_actual_cost":0.6}`,
		`{}`,
		`null`,
	} {
		t.Run(payload, func(t *testing.T) {
			var stats AccountUsageStats
			if err := json.Unmarshal([]byte(payload), &stats); err != nil {
				t.Fatal(err)
			}
			if stats.Available {
				t.Fatalf("incomplete stats marked available: %+v", stats)
			}
		})
	}
}

func TestCheckinStatusAndReasonPreserved(t *testing.T) {
	client, err := New(OfficialSiteURL, "")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		if request.URL.Path == "/api/v1/user/checkin" {
			return &http.Response{StatusCode: http.StatusConflict, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":409,"reason":"DAILY_CHECKIN_ALREADY_CHECKED_IN","message":"already checked in today"}`)), Request: request}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"code":0,"message":"success","data":{"checked_in":true,"today":"2026-08-30"}}`)), Request: request}, nil
	})}
	_, err = client.Checkin(context.Background(), "access-token")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Reason != "DAILY_CHECKIN_ALREADY_CHECKED_IN" {
		t.Fatalf("expected typed checkin reason, got %v", err)
	}
	status, err := client.CheckinStatus(context.Background(), "access-token")
	if err != nil || !status.CheckedIn || len(paths) != 2 || paths[1] != "/api/v1/user/checkin/status" {
		t.Fatalf("unexpected status result: %+v %v paths=%v", status, err, paths)
	}
}

func TestAuthenticatedRedirectsNeverCrossOriginOrDowngrade(t *testing.T) {
	for _, location := range []string{
		"https://attacker.example/collect",
		"http://ai.clol.site/api/v1/user/profile",
	} {
		t.Run(location, func(t *testing.T) {
			client, err := New(OfficialSiteURL, "")
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				if calls > 1 {
					t.Fatalf("credential-bearing redirect reached %s", request.URL)
				}
				if request.Header.Get("Authorization") != "Bearer access-token" {
					t.Fatalf("initial authenticated request was malformed: %v", request.Header)
				}
				return &http.Response{
					StatusCode: http.StatusFound,
					Header:     http.Header{"Location": []string{location}},
					Body:       io.NopCloser(strings.NewReader("redirect")),
					Request:    request,
				}, nil
			})
			if _, err := client.Profile(context.Background(), "access-token"); err == nil || !strings.Contains(err.Error(), "redirect") {
				t.Fatalf("expected redirect policy error, got %v", err)
			}
			if calls != 1 {
				t.Fatalf("redirect target was contacted: %d calls", calls)
			}
		})
	}
}

func TestAuthenticatedRequestRejectsPlaintextBeforeTransport(t *testing.T) {
	client, err := New("http://ai.clol.site", "")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return nil, nil
	})
	if _, err := client.Usage(context.Background(), "sk-secret"); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("expected HTTPS rejection, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("plaintext authenticated request reached transport: %d", calls)
	}
}

func TestImageTaskAssetsReturnOnlySafeHTTPSURLs(t *testing.T) {
	task := ImageTask{
		Data: []ImageAsset{
			{URL: "https://cdn.example/image.png", RevisedPrompt: "keep"},
			{URL: "http://cdn.example/insecure.png"},
			{URL: "https://user:secret@cdn.example/private.png"},
			{URL: "javascript:alert(1)"},
			{URL: "data:image/png;base64,AAAA"},
			{URL: "https://cdn.example/%0Aheader"},
		},
	}
	assets := task.Assets()
	if len(assets) != 1 || assets[0].URL != "https://cdn.example/image.png" || assets[0].RevisedPrompt != "keep" {
		t.Fatalf("unexpected filtered assets: %+v", assets)
	}

	fallback := ImageTask{ImageURL: "http://cdn.example/image.png", Result: json.RawMessage(`{"data":[{"url":"https://cdn.example/result.png"},{"url":"https://user@cdn.example/private.png"}]}`)}
	assets = fallback.Assets()
	if len(assets) != 1 || assets[0].URL != "https://cdn.example/result.png" {
		t.Fatalf("unexpected result-envelope assets: %+v", assets)
	}
}

func TestScopedAccountEndpointsUseEnvelopeAndDPoP(t *testing.T) {
	client, err := New(OfficialSiteURL, "")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewDeviceProof()
	if err != nil {
		t.Fatal(err)
	}
	client.SetDeviceProof(proof, "nonce")
	var requests []*http.Request
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request)
		var body string
		switch {
		case strings.HasSuffix(request.URL.Path, "/keys"):
			body = `{"code":0,"message":"success","data":{"items":[{"id":7,"name":"main","key":"sk-secret","status":"active"}],"total":1,"page":1,"page_size":100,"pages":1}}`
		case strings.HasSuffix(request.URL.Path, "/user/devices"):
			body = `{"code":0,"message":"success","data":{"devices":[{"device_id":"dev-1","device_name":"Mac","scopes":["profile"]}]}}`
		default:
			body = `{"code":0,"message":"success","data":{}}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"DPoP-Nonce": []string{"nonce-next"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	keys, err := client.ListAPIKeys(context.Background(), "access", 1, 100, "")
	if err != nil || len(keys.Items) != 1 || keys.Items[0].Key != "sk-secret" {
		t.Fatalf("unexpected key page: %+v %v", keys, err)
	}
	devices, err := client.ListDevices(context.Background(), "access")
	if err != nil || len(devices) != 1 || devices[0].DeviceID != "dev-1" {
		t.Fatalf("unexpected devices: %+v %v", devices, err)
	}
	if len(requests) != 2 || requests[0].Header.Get("DPoP") == "" || requests[1].Header.Get("DPoP") == "" {
		t.Fatalf("scoped requests must carry DPoP: %d", len(requests))
	}
	var proofClaims struct {
		HTU string `json:"htu"`
	}
	parts := strings.Split(requests[0].Header.Get("DPoP"), ".")
	if len(parts) != 3 {
		t.Fatalf("invalid DPoP compact token")
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(decoded, &proofClaims); err != nil {
		t.Fatal(err)
	}
	if proofClaims.HTU != OfficialSiteURL+"/api/v1/keys" {
		t.Fatalf("query must not enter DPoP htu: %q", proofClaims.HTU)
	}
}

func TestIntegrationProfilesUsesOwnedKeyQueryAndDPoP(t *testing.T) {
	client, err := New(OfficialSiteURL, "")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewDeviceProof()
	if err != nil {
		t.Fatal(err)
	}
	client.SetDeviceProof(proof, "nonce")
	var captured *http.Request
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured = request
		body := `{"code":0,"message":"success","data":{"key_specific":true,"api_key":{"id":42,"name":"images","status":"active","available":true},"profiles":[{"id":"openai-images","client_id":"sub2api-desktop","audience":"sub2api-api","auth":"api_key","grant_type":"device_code","refresh_grant_type":"refresh_token","base_path":"/v1","api_key_id":42,"available":true,"async_capability":"use_client_capabilities.async_images","endpoints":["/images/generations"],"configuration":["api_base_url","api_key"]}]}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"DPoP-Nonce": []string{"nonce-next"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}

	result, err := client.IntegrationProfiles(context.Background(), "access-token", 42)
	if err != nil {
		t.Fatal(err)
	}
	if !result.KeySpecific || result.APIKey.ID != 42 || len(result.Profiles) != 1 || result.Profiles[0].APIKeyID != 42 || result.Profiles[0].AsyncCapability == "" {
		t.Fatalf("unexpected integration profile response: %+v", result)
	}
	if captured == nil {
		t.Fatal("integration profile request was not captured")
	}
	if captured.Method != http.MethodGet || captured.URL.Path != "/api/v1/client/integration-profiles" || captured.URL.Query().Get("api_key_id") != "42" {
		t.Fatalf("unexpected integration profile request: %s %s", captured.Method, captured.URL)
	}
	if captured.Header.Get("Authorization") != "DPoP access-token" || captured.Header.Get("DPoP") == "" {
		t.Fatalf("request must use proof-bound authorization: %v", captured.Header)
	}
	parts := strings.Split(captured.Header.Get("DPoP"), ".")
	if len(parts) != 3 {
		t.Fatalf("invalid DPoP compact token")
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		HTU string `json:"htu"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.HTU != OfficialSiteURL+"/api/v1/client/integration-profiles" {
		t.Fatalf("query must not enter DPoP htu: %q", claims.HTU)
	}
}

func TestIntegrationProfilesValidatesAuthenticationInputs(t *testing.T) {
	client, err := New(OfficialSiteURL, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		token string
		id    int64
	}{
		{name: "missing token", id: 1},
		{name: "missing id", token: "access-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := client.IntegrationProfiles(context.Background(), tc.token, tc.id); err == nil {
				t.Fatal("expected input validation error")
			}
		})
	}
}

func TestDeleteImageHistoryUsesOwnedSessionEndpoint(t *testing.T) {
	client, err := New(OfficialSiteURL, "")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := NewDeviceProof()
	if err != nil {
		t.Fatal(err)
	}
	client.SetDeviceProof(proof, "nonce")
	var captured *http.Request
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured = request
		return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})}

	if err := client.DeleteImageHistory(context.Background(), "session-token", "imgtask_terminal"); err != nil {
		t.Fatal(err)
	}
	if captured == nil || captured.Method != http.MethodDelete || captured.URL.Path != "/api/v1/user/image-tasks/imgtask_terminal" {
		t.Fatalf("unexpected delete request: %#v", captured)
	}
	if captured.Header.Get("Authorization") != "DPoP session-token" || captured.Header.Get("DPoP") == "" {
		t.Fatalf("delete request missing session proof: %v", captured.Header)
	}
}

func TestEditImageBuildsValidatedMultipartBody(t *testing.T) {
	client, err := New(OfficialSiteURL, "")
	if err != nil {
		t.Fatal(err)
	}
	var captured *http.Request
	var capturedBody []byte
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured = request
		capturedBody, _ = io.ReadAll(request.Body)
		return &http.Response{StatusCode: http.StatusAccepted, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"task-1","task_id":"task-1","status":"processing"}`)), Request: request}, nil
	})}
	tinyPNG := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	task, err := client.EditImage(context.Background(), "sk-test", ImageEditRequest{
		Model: "gpt-image-2", Prompt: "make it warmer", N: 1,
		Images: []ImageEditUpload{{Name: "source.png", ContentType: "image/png", DataURL: tinyPNG}},
		Mask:   &ImageEditUpload{Name: "mask.png", ContentType: "image/png", DataURL: tinyPNG},
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.TaskID != "task-1" || captured == nil {
		t.Fatalf("unexpected task/request: %+v %#v", task, captured)
	}
	if captured.Method != http.MethodPost || captured.URL.Path != "/v1/images/edits/async" {
		t.Fatalf("unexpected request: %s %s", captured.Method, captured.URL)
	}
	if captured.Header.Get("Authorization") != "Bearer sk-test" {
		t.Fatalf("missing API key header: %v", captured.Header)
	}
	mediaType, params, err := mime.ParseMediaType(captured.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("unexpected multipart content type: %q (%v)", captured.Header.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(strings.NewReader(string(capturedBody)), params["boundary"])
	fields := map[string]string{}
	files := map[string]string{}
	for {
		part, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		data, readErr := io.ReadAll(part)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if part.FileName() != "" {
			files[part.FormName()] = part.FileName()
		} else {
			fields[part.FormName()] = string(data)
		}
	}
	if fields["model"] != "gpt-image-2" || fields["prompt"] != "make it warmer" || fields["n"] != "1" {
		t.Fatalf("missing multipart fields: %#v", fields)
	}
	if files["image"] != "source.png" || files["mask"] != "mask.png" {
		t.Fatalf("missing multipart files: %#v", files)
	}
}

func TestEditImageRejectsMismatchedDataURL(t *testing.T) {
	client, err := New(OfficialSiteURL, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.EditImage(context.Background(), "sk-test", ImageEditRequest{
		Model: "gpt-image-2", Prompt: "edit", Images: []ImageEditUpload{{ContentType: "image/png", DataURL: "data:image/png;base64,SGVsbG8="}},
	})
	if err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("expected data URL validation error, got %v", err)
	}
}

func TestEditImageStreamsNativeFilesWithoutDataURLBuffer(t *testing.T) {
	client, err := New(OfficialSiteURL, "")
	if err != nil {
		t.Fatal(err)
	}
	tinyPNG, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.png")
	if err := os.WriteFile(path, tinyPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	var capturedBody []byte
	client.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		capturedBody, _ = io.ReadAll(request.Body)
		return &http.Response{StatusCode: http.StatusAccepted, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"task-native","status":"processing"}`)), Request: request}, nil
	})}
	task, err := client.EditImage(context.Background(), "sk-test", ImageEditRequest{
		Model: "gpt-image-2", Prompt: "native", N: 1,
		Files: []ImageEditFile{{Name: "source.png", ContentType: "image/png", Path: path}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.TaskID != "task-native" || len(capturedBody) == 0 {
		t.Fatalf("unexpected native task/body: %+v %d", task, len(capturedBody))
	}
}
