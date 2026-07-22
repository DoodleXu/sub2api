package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// pngBytes is a minimal payload whose signature makes http.DetectContentType
// report image/png.
var pngBytes = []byte("\x89PNG\r\n\x1a\nfake-png-payload")

type imageRoundTripFunc func(*http.Request) (*http.Response, error)

func (f imageRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type savedImage struct {
	key         string
	contentType string
	data        []byte
}

type fakeImageStorage struct {
	saved      []savedImage
	deleted    []string
	url        string
	err        error
	deleteErr  error
	saveErrAt  int
	beforeSave func(key string)
}

func TestImageDownloadHTTPClientUsesSafeTransportFallback(t *testing.T) {
	client := imageDownloadHTTPClientFrom(imageRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unexpected fallback RoundTrip call")
	}))

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.Nil(t, transport.Proxy, "environment proxies must stay disabled")
	require.NotNil(t, transport.DialContext, "SSRF validation must run before every connection")
	require.True(t, transport.ForceAttemptHTTP2)
	require.Equal(t, 10*time.Second, transport.TLSHandshakeTimeout)
}

func (f *fakeImageStorage) Save(_ context.Context, key, contentType string, data []byte) (string, error) {
	if f.beforeSave != nil {
		f.beforeSave(key)
	}
	if f.err != nil || (f.saveErrAt > 0 && len(f.saved)+1 == f.saveErrAt) {
		if f.err == nil {
			return "", errors.New("injected save failure")
		}
		return "", f.err
	}
	f.saved = append(f.saved, savedImage{key: key, contentType: contentType, data: append([]byte(nil), data...)})
	if f.url != "" {
		return f.url, nil
	}
	return "https://cdn.test/" + key, nil
}

func (f *fakeImageStorage) Delete(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return f.deleteErr
}

func TestImageResultUploaderRewritesB64JSON(t *testing.T) {
	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)

	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	result := json.RawMessage(`{"created":1,"data":[{"b64_json":"` + b64 + `","revised_prompt":"a cat"}]}`)

	out, err := uploader.Rewrite(context.Background(), "imgtask_abc", result)
	require.NoError(t, err)

	require.Len(t, storage.saved, 1)
	require.Regexp(t, `^images/imgtask_abc-[0-9a-f]{12}-0\.png$`, storage.saved[0].key)
	require.Equal(t, "image/png", storage.saved[0].contentType)
	require.Equal(t, pngBytes, storage.saved[0].data)

	var parsed struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &parsed))
	require.Len(t, parsed.Data, 1)
	require.Contains(t, string(parsed.Data[0]["url"]), "https://cdn.test/images/imgtask_abc-")
	_, hasB64 := parsed.Data[0]["b64_json"]
	require.False(t, hasB64, "b64_json must be stripped after offload")
	require.JSONEq(t, `"a cat"`, string(parsed.Data[0]["revised_prompt"]), "unrelated fields preserved")
}

func TestImageResultUploaderRewritesURL(t *testing.T) {
	client := &http.Client{Transport: imageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(strings.NewReader(string(pngBytes))),
			Request:    req,
		}, nil
	})}

	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 0, client)

	result := json.RawMessage(`{"created":1,"data":[{"url":"https://images.example.test/pic.png"}]}`)
	out, err := uploader.Rewrite(context.Background(), "imgtask_xyz", result)
	require.NoError(t, err)

	require.Len(t, storage.saved, 1)
	require.Equal(t, pngBytes, storage.saved[0].data)
	require.Equal(t, "image/png", storage.saved[0].contentType)

	var parsed struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &parsed))
	require.Contains(t, string(parsed.Data[0]["url"]), "https://cdn.test/images/imgtask_xyz-")
}

func TestImageResultUploaderRejectsUnsafeDownloadURL(t *testing.T) {
	called := false
	client := &http.Client{Transport: imageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("must not be called")
	})}
	uploader := NewImageResultUploader(&fakeImageStorage{}, "images/", 0, client)

	for _, rawURL := range []string{
		"http://images.example.test/pic.png",
		"https://127.0.0.1/pic.png",
		"https://169.254.169.254/latest/meta-data/",
		"https://[::1]/pic.png",
	} {
		result := json.RawMessage(`{"data":[{"url":"` + rawURL + `"}]}`)
		_, err := uploader.Rewrite(context.Background(), "imgtask_unsafe", result)
		require.Error(t, err, rawURL)
	}
	require.False(t, called)
}

func TestImageResultUploaderRejectsNonImageResponse(t *testing.T) {
	client := &http.Client{Transport: imageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(strings.NewReader("<html>not an image</html>")),
			Request:    req,
		}, nil
	})}
	uploader := NewImageResultUploader(&fakeImageStorage{}, "images/", 0, client)

	_, err := uploader.Rewrite(context.Background(), "imgtask_html", json.RawMessage(`{"data":[{"url":"https://images.example.test/pic.png"}]}`))
	require.ErrorContains(t, err, "not a supported image")
}

func TestImageResultUploaderAcceptsValidImageWithGenericContentType(t *testing.T) {
	client := &http.Client{Transport: imageRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/octet-stream"}},
			Body:       io.NopCloser(strings.NewReader(string(pngBytes))),
			Request:    req,
		}, nil
	})}
	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 0, client)

	_, err := uploader.Rewrite(context.Background(), "imgtask_generic", json.RawMessage(`{"data":[{"url":"https://images.example.test/pic"}]}`))
	require.NoError(t, err)
	require.Len(t, storage.saved, 1)
	require.Equal(t, "image/png", storage.saved[0].contentType)
}

func TestImageResultUploaderRejectsNonImageBase64(t *testing.T) {
	uploader := NewImageResultUploader(&fakeImageStorage{}, "images/", 0, nil)
	b64 := base64.StdEncoding.EncodeToString([]byte("not an image"))

	_, err := uploader.Rewrite(context.Background(), "imgtask_b64", json.RawMessage(`{"data":[{"b64_json":"`+b64+`"}]}`))
	require.ErrorContains(t, err, "not a supported image")
}

func TestImageResultUploaderPropagatesStorageError(t *testing.T) {
	storage := &fakeImageStorage{err: errors.New("bucket unreachable")}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)

	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	result := json.RawMessage(`{"data":[{"b64_json":"` + b64 + `"}]}`)

	_, err := uploader.Rewrite(context.Background(), "imgtask_err", result)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bucket unreachable")
}

func TestImageResultUploaderCleansUpPartialMultiImageUpload(t *testing.T) {
	storage := &fakeImageStorage{saveErrAt: 2}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)

	_, err := uploader.Rewrite(context.Background(), "imgtask_partial", json.RawMessage(
		`{"data":[{"b64_json":"`+b64+`"},{"b64_json":"`+b64+`"}]}`,
	))

	require.ErrorContains(t, err, "injected save failure")
	require.Len(t, storage.saved, 1)
	require.Len(t, storage.deleted, 2, "both committed and ambiguous attempt keys must be deleted")
	require.Equal(t, storage.saved[0].key, storage.deleted[0])
	require.Regexp(t, `^images/imgtask_partial-[0-9a-f]{12}-1\.png$`, storage.deleted[1])
}

func TestImageResultUploaderNilStoragePassthrough(t *testing.T) {
	var uploader *ImageResultUploader
	result := json.RawMessage(`{"data":[{"url":"https://example.test/x.png"}]}`)
	out, err := uploader.Rewrite(context.Background(), "imgtask_nil", result)
	require.NoError(t, err)
	require.JSONEq(t, string(result), string(out))
}

func TestImageTaskServiceCompleteOffloadsToStorage(t *testing.T) {
	store := &imageTaskMemoryStore{}
	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	svc := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)
	require.True(t, svc.Enabled())

	owner := ImageTaskOwner{UserID: 1, APIKeyID: 2}
	created, err := svc.Create(context.Background(), owner)
	require.NoError(t, err)

	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	result := json.RawMessage(`{"created":1,"data":[{"b64_json":"` + b64 + `"}]}`)
	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK, result))

	got, err := svc.Get(context.Background(), owner, created.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusCompleted, got.Status)
	require.Contains(t, got.ImageURL, "https://cdn.test/images/"+created.ID+"-")
	require.NotContains(t, string(got.Result), "b64_json", "large base64 must not be persisted to Redis")
	require.Len(t, storage.saved, 1)
}

func TestImageTaskServicePinsUploaderForAcceptedTask(t *testing.T) {
	store := &imageTaskMemoryStore{}
	firstStorage := &fakeImageStorage{}
	secondStorage := &fakeImageStorage{}
	firstUploader := NewImageResultUploader(firstStorage, "first/", 0, nil)
	secondUploader := NewImageResultUploader(secondStorage, "second/", 0, nil)
	currentUploader := firstUploader
	enabled := true
	svc := NewImageTaskServiceWithResolver(store, func() (*ImageResultUploader, bool) {
		return currentUploader, enabled
	}, time.Hour, time.Minute)
	defer svc.Close()

	owner := ImageTaskOwner{UserID: 1, APIKeyID: 2}
	created, err := svc.Create(context.Background(), owner)
	require.NoError(t, err)

	// A hot configuration change after admission must not redirect this task's
	// result or compensation work to a different object store.
	currentUploader = secondUploader
	enabled = false
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK,
		json.RawMessage(`{"data":[{"b64_json":"`+b64+`"}]}`)))

	require.Len(t, firstStorage.saved, 1)
	require.Empty(t, secondStorage.saved)
	require.NotContains(t, string(store.task.Result), "b64_json")
	require.Contains(t, string(store.task.Result), "https://cdn.test/first/")
}

func TestImageTaskServiceDynamicModeNeverPersistsRawResultWithoutUploader(t *testing.T) {
	store := &imageTaskMemoryStore{}
	svc := NewImageTaskServiceWithResolver(store, func() (*ImageResultUploader, bool) {
		return nil, false
	}, time.Hour, time.Minute)
	defer svc.Close()

	_, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2})
	require.ErrorIs(t, err, ErrImageTaskUnavailable)
	require.Nil(t, store.task)
}

func TestImageTaskServicePersistsObjectManifestBeforeUpload(t *testing.T) {
	store := &imageTaskMemoryStore{}
	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	svc := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)
	created, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2})
	require.NoError(t, err)

	storage.beforeSave = func(key string) {
		require.Equal(t, ImageTaskStatusProcessing, store.task.Status)
		require.Contains(t, store.task.PendingObjectKeys, key, "cleanup key must be durable before Save starts")
	}
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK, json.RawMessage(`{"data":[{"b64_json":"`+b64+`"}]}`)))
}

func TestImageTaskServiceCompleteDoesNotUploadWhenManifestTransitionLoses(t *testing.T) {
	store := &imageTaskMemoryStore{forceTransitionMiss: true}
	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	svc := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)
	created, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2})
	require.NoError(t, err)

	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	err = svc.Complete(context.Background(), created.ID, http.StatusOK, json.RawMessage(`{"data":[{"b64_json":"`+b64+`"}]}`))

	require.NoError(t, err)
	require.Empty(t, storage.saved)
	require.Empty(t, storage.deleted)
}

func TestImageTaskServiceCompletePreservesObjectWhenTransitionCommittedBeforeDeadline(t *testing.T) {
	store := &imageTaskMemoryStore{transitionErrAfterCommit: context.DeadlineExceeded}
	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	svc := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)
	created, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2})
	require.NoError(t, err)

	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	err = svc.Complete(context.Background(), created.ID, http.StatusOK, json.RawMessage(`{"data":[{"b64_json":"`+b64+`"}]}`))

	require.NoError(t, err)
	require.Len(t, storage.saved, 1)
	require.Empty(t, storage.deleted, "a committed completion must keep its referenced object")
	require.Equal(t, ImageTaskStatusCompleted, store.task.Status)
	require.Contains(t, string(store.task.Result), storage.saved[0].key)
}

func TestImageTaskServiceFailedFallbackCleansObjectsAfterCompletionOutcomeRemainsUnknown(t *testing.T) {
	store := &imageTaskMemoryStore{transitionErrNoCommit: context.DeadlineExceeded}
	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	svc := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)
	created, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2})
	require.NoError(t, err)

	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	err = svc.Complete(context.Background(), created.ID, http.StatusOK, json.RawMessage(`{"data":[{"b64_json":"`+b64+`"}]}`))

	require.ErrorIs(t, err, ErrImageTaskUnavailable)
	require.Len(t, storage.saved, 1)
	require.Empty(t, storage.deleted, "an ambiguous completion must preserve objects until a terminal CAS wins")
	require.Equal(t, ImageTaskStatusProcessing, store.task.Status)
	require.Equal(t, []string{storage.saved[0].key}, store.task.PendingObjectKeys)

	require.NoError(t, svc.Fail(context.Background(), created.ID, http.StatusBadGateway, json.RawMessage(`{"error":{"message":"completion failed"}}`)))
	require.Equal(t, ImageTaskStatusFailed, store.task.Status)
	require.Equal(t, []string{storage.saved[0].key}, storage.deleted)
	require.Empty(t, store.task.PendingObjectKeys)
}

func TestImageTaskServiceCompleteCleansUpWhenCompletionContextExpires(t *testing.T) {
	store := &imageTaskMemoryStore{respectContext: true}
	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	svc := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)
	created, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	err = svc.Complete(ctx, created.ID, http.StatusOK, json.RawMessage(`{"data":[{"b64_json":"`+b64+`"}]}`))

	require.ErrorIs(t, err, ErrImageTaskUnavailable)
	require.Empty(t, storage.saved)
	require.Empty(t, storage.deleted)
}

func TestImageTaskServiceCompleteOffloadFailureMarksFailed(t *testing.T) {
	store := &imageTaskMemoryStore{}
	storage := &fakeImageStorage{err: errors.New("bucket unreachable")}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	svc := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)

	owner := ImageTaskOwner{UserID: 1, APIKeyID: 2}
	created, err := svc.Create(context.Background(), owner)
	require.NoError(t, err)

	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	result := json.RawMessage(`{"data":[{"b64_json":"` + b64 + `"}]}`)
	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK, result))

	got, err := svc.Get(context.Background(), owner, created.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusFailed, got.Status)
	require.Equal(t, http.StatusBadGateway, got.HTTPStatus)
	require.Contains(t, string(got.Error), "object storage")
	require.NotContains(t, string(got.Result), "b64_json", "failed offload must not persist base64 to Redis")
	require.Len(t, storage.deleted, 1, "ambiguous Save failures must compensate the unique attempt key")
}

func TestImageTaskServicePollRetriesFailedPendingObjectCleanup(t *testing.T) {
	store := &imageTaskMemoryStore{}
	storage := &fakeImageStorage{deleteErr: errors.New("temporary delete failure")}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	svc := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)
	owner := ImageTaskOwner{UserID: 1, APIKeyID: 2}
	created, err := svc.Create(context.Background(), owner)
	require.NoError(t, err)

	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	store.transitionErrNoCommit = context.DeadlineExceeded
	err = svc.Complete(context.Background(), created.ID, http.StatusOK, json.RawMessage(`{"data":[{"b64_json":"`+b64+`"}]}`))
	require.ErrorIs(t, err, ErrImageTaskUnavailable)
	store.transitionErrNoCommit = nil
	err = svc.Fail(context.Background(), created.ID, http.StatusBadGateway, json.RawMessage(`{"error":{"message":"failed"}}`))
	require.ErrorContains(t, err, "temporary delete failure")
	require.NotEmpty(t, store.task.PendingObjectKeys)

	storage.deleteErr = nil
	_, err = svc.Get(context.Background(), owner, created.ID)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		svc.cleanupMu.Lock()
		defer svc.cleanupMu.Unlock()
		_, running := svc.cleanupRunning[created.ID]
		return !running
	}, time.Second, 10*time.Millisecond)
	require.Empty(t, store.task.PendingObjectKeys)
	require.Len(t, storage.deleted, 2)
}

func TestImageTaskServiceClosePreventsPollCleanupRegistration(t *testing.T) {
	owner := ImageTaskOwner{UserID: 1, APIKeyID: 2}
	store := &imageTaskMemoryStore{task: &ImageTaskRecord{
		ID: "imgtask_closed", UserID: owner.UserID, APIKeyID: owner.APIKeyID,
		Status: ImageTaskStatusFailed, PendingObjectKeys: []string{"images/pending.png"},
	}}
	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	svc := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)
	svc.Close()

	_, err := svc.Get(context.Background(), owner, store.task.ID)
	require.NoError(t, err)
	require.Empty(t, storage.deleted, "polling after shutdown must not register new cleanup work")
	svc.cleanupMu.Lock()
	defer svc.cleanupMu.Unlock()
	require.Empty(t, svc.cleanupRunning)
}
