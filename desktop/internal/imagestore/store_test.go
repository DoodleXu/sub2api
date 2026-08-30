package imagestore

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestDownloadValidatesImageAndWritesAtomically(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	payload := testPNG(t, 2, 3)
	store.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{"image/png; charset=binary"}},
			Body:          io.NopCloser(bytes.NewReader(payload)),
			Request:       request,
			ContentLength: int64(len(payload)),
		}, nil
	})}

	asset, err := store.Download(context.Background(), "https://images.example.test/generated", nil, AssetMetadata{Name: "result.exe"})
	if err != nil {
		t.Fatal(err)
	}
	if asset.MimeType != "image/png" || asset.Name != "result.png" || asset.Bytes != int64(len(payload)) {
		t.Fatalf("unexpected asset metadata: %+v", asset)
	}
	data, err := os.ReadFile(asset.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatal("stored image differs from downloaded bytes")
	}
	if mode := fileMode(t, asset.Path); mode&0077 != 0 {
		t.Fatalf("asset permissions are too broad: %o", mode)
	}
	serialized, err := json.Marshal(asset)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), asset.Path) || strings.Contains(string(serialized), `"path"`) {
		t.Fatalf("local asset path crossed the JSON/Wails boundary: %s", serialized)
	}
	metadataBytes, err := os.ReadFile(filepath.Join(root, asset.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(metadataBytes), `"path"`) {
		t.Fatalf("private disk metadata lost path needed for local reopen: %s", metadataBytes)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") || strings.HasPrefix(entry.Name(), ".atomic-") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestSaveDataURLValidatesAndKeepsPathPrivate(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	dataURL := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	asset, err := store.SaveDataURL(context.Background(), dataURL, AssetMetadata{Name: "inline.png"})
	if err != nil {
		t.Fatal(err)
	}
	if asset.MimeType != "image/png" || asset.Name != "inline.png" || asset.Path == "" {
		t.Fatalf("unexpected asset: %+v", asset)
	}
	serialized, err := json.Marshal(asset)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), asset.Path) || strings.Contains(string(serialized), `"path"`) {
		t.Fatalf("path crossed asset boundary: %s", serialized)
	}
}

func TestScopedFileStoreSeparatesOwnersAndKeepsLegacyRowsUnowned(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ownerA := OwnerHashForSubject("user:asset-a")
	ownerB := OwnerHashForSubject("user:asset-b")
	payload := testPNGBytes(1, 1)
	legacy, err := store.Save(ctx, bytes.NewReader(payload), AssetMetadata{Name: "legacy.png"})
	if err != nil {
		t.Fatal(err)
	}
	assetA, err := store.SaveForOwner(ctx, ownerA, bytes.NewReader(payload), AssetMetadata{Name: "a.png"})
	if err != nil {
		t.Fatal(err)
	}
	assetB, err := store.SaveForOwner(ctx, ownerB, bytes.NewReader(payload), AssetMetadata{Name: "b.png"})
	if err != nil {
		t.Fatal(err)
	}
	if assetA.OwnerHash != ownerA || assetB.OwnerHash != ownerB || legacy.OwnerHash != "" {
		t.Fatalf("unexpected owner metadata: legacy=%q a=%q b=%q", legacy.OwnerHash, assetA.OwnerHash, assetB.OwnerHash)
	}

	ownedA, err := store.ListForOwner(ctx, ownerA)
	if err != nil {
		t.Fatal(err)
	}
	if len(ownedA) != 1 || ownedA[0].ID != assetA.ID || ownedA[0].OwnerHash != ownerA {
		t.Fatalf("owner A saw wrong assets: %+v", ownedA)
	}
	ownedB, err := store.ListForOwner(ctx, ownerB)
	if err != nil {
		t.Fatal(err)
	}
	if len(ownedB) != 1 || ownedB[0].ID != assetB.ID || ownedB[0].OwnerHash != ownerB {
		t.Fatalf("owner B saw wrong assets: %+v", ownedB)
	}
	all, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("legacy unscoped list changed unexpectedly: %+v", all)
	}

	if err := store.DeleteForOwner(ctx, ownerB, assetA.ID); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("cross-owner delete error = %v, want not found", err)
	}
	if err := store.DeleteForOwner(ctx, ownerA, "0123456789abcdef0123456789abcdef"); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("missing scoped delete error = %v, want not found", err)
	}
	if _, err := os.Stat(assetA.Path); err != nil {
		t.Fatalf("cross-owner delete removed owner A asset: %v", err)
	}
	if err := store.DeleteForOwner(ctx, ownerA, assetA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(assetA.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("same-owner delete left asset: %v", err)
	}
	if items, err := store.ListForOwner(ctx, ownerA); err != nil {
		t.Fatal(err)
	} else if len(items) != 0 {
		t.Fatalf("deleted owner A asset remained visible: %+v", items)
	}
}

func TestScopedFileStoreRejectsInvalidOwnerBeforeWriting(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveForOwner(context.Background(), "user:raw", bytes.NewReader(testPNGBytes(1, 1)), AssetMetadata{Name: "bad.png"}); !errors.Is(err, ErrInvalidTaskOwner) {
		t.Fatalf("invalid owner error = %v, want ErrInvalidTaskOwner", err)
	}
	if entries, readErr := os.ReadDir(store.root); readErr != nil {
		t.Fatal(readErr)
	} else if len(entries) != 0 {
		t.Fatalf("invalid owner left files: %+v", entries)
	}
}

func TestDownloadRejectsUnsafeURLsBeforeTransport(t *testing.T) {
	calls := 0
	store, err := NewFileStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	store.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("transport should not be called")
	})}
	for _, source := range []string{
		"http://images.example.test/a.png",
		"https://user:password@images.example.test/a.png",
		"https://127.0.0.1/a.png",
		"https://[::1]/a.png",
		"https://169.254.169.254/latest/meta-data/",
		"https://service.local/a.png",
		"https://metadata.google.internal/a.png",
	} {
		if _, err := store.Download(context.Background(), source, nil, AssetMetadata{}); err == nil {
			t.Errorf("expected unsafe URL rejection for %s", source)
		}
	}
	if calls != 0 {
		t.Fatalf("unsafe URLs reached transport %d times", calls)
	}
}

func TestDownloadRejectsRedirectToHTTPOrPrivateAddress(t *testing.T) {
	for _, location := range []string{
		"http://cdn.example.test/final",
		"https://127.0.0.1/final",
	} {
		store, err := NewFileStore(t.TempDir(), 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		store.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{location}},
				Body:       io.NopCloser(strings.NewReader("redirect")),
				Request:    request,
			}, nil
		})}
		if _, err := store.Download(context.Background(), "https://images.example.test/start", nil, AssetMetadata{}); err == nil {
			t.Errorf("expected redirect rejection for %s", location)
		}
		if calls != 1 {
			t.Errorf("redirect target was unexpectedly requested (%s): %d calls", location, calls)
		}
	}
}

func TestDownloadStripsHeadersOnCrossOriginRedirect(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	payload := testPNG(t, 1, 1)
	var redirectedHeaders http.Header
	store.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Hostname() == "images.example.test" {
			return &http.Response{
				StatusCode: http.StatusTemporaryRedirect,
				Header:     http.Header{"Location": []string{"https://cdn.example.test/final"}},
				Body:       io.NopCloser(strings.NewReader("redirect")),
				Request:    request,
			}, nil
		}
		redirectedHeaders = request.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(bytes.NewReader(payload)),
			Request:    request,
		}, nil
	})}
	if _, err := store.Download(context.Background(), "https://images.example.test/start", map[string]string{
		"Authorization": "Bearer secret",
		"X-API-Key":     "sk-secret",
		"Accept":        "image/*",
	}, AssetMetadata{}); err != nil {
		t.Fatal(err)
	}
	if redirectedHeaders.Get("Authorization") != "" || redirectedHeaders.Get("X-API-Key") != "" {
		t.Fatalf("credential header leaked across redirect: %v", redirectedHeaders)
	}
	if redirectedHeaders.Get("Accept") != "image/*" {
		t.Fatalf("safe Accept header was not preserved: %v", redirectedHeaders)
	}
	if redirectedHeaders.Get("Referer") != "" {
		t.Fatalf("signed source URL leaked through Referer: %v", redirectedHeaders)
	}
}

func TestDownloadRejectsMIMEMagicAndDecodeMismatch(t *testing.T) {
	tests := []struct {
		name         string
		contentType  string
		metadataType string
		body         []byte
	}{
		{name: "html body", contentType: "image/png", body: []byte("<html>bad</html>")},
		{name: "declared MIME mismatch", contentType: "image/jpeg", body: testPNGBytes(1, 1)},
		{name: "metadata MIME mismatch", contentType: "image/png", metadataType: "image/jpeg", body: testPNGBytes(1, 1)},
		{name: "invalid PNG header", contentType: "image/png", body: []byte("\x89PNG\r\n\x1a\nnot-a-png")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewFileStore(t.TempDir(), 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			store.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{test.contentType}},
					Body:       io.NopCloser(bytes.NewReader(test.body)),
					Request:    request,
				}, nil
			})}
			if _, err := store.Download(context.Background(), "https://images.example.test/image", nil, AssetMetadata{MimeType: test.metadataType}); err == nil {
				t.Fatal("expected image validation error")
			}
			if files, _ := os.ReadDir(store.root); len(files) != 0 {
				t.Fatalf("failed download left files: %v", files)
			}
		})
	}
}

func TestDownloadRejectsResponseSizeAndImageBombDimensions(t *testing.T) {
	tooLarge := testPNGBytes(1, 1)
	store, err := NewFileStore(t.TempDir(), int64(len(tooLarge)-1))
	if err != nil {
		t.Fatal(err)
	}
	store.httpClient = imageResponseClient(tooLarge, "image/png")
	if _, err := store.Download(context.Background(), "https://images.example.test/large", nil, AssetMetadata{}); err == nil {
		t.Fatal("expected response size rejection")
	}

	for _, dimensions := range [][2]int{{16385, 1}, {10001, 10000}} {
		store, err := NewFileStore(t.TempDir(), 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		body := testPNGBytes(dimensions[0], dimensions[1])
		store.httpClient = imageResponseClient(body, "image/png")
		if _, err := store.Download(context.Background(), "https://images.example.test/bomb", nil, AssetMetadata{}); err == nil {
			t.Errorf("expected dimension/pixel rejection for %v", dimensions)
		}
	}
}

func TestDownloadHonorsContextCancellation(t *testing.T) {
	store, err := NewFileStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	store.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Download(ctx, "https://images.example.test/cancel", nil, AssetMetadata{}); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestOpenRejectsMetadataSymlinkAsset(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	payload := testPNGBytes(1, 1)
	store.httpClient = imageResponseClient(payload, "image/png")
	asset, err := store.Download(context.Background(), "https://images.example.test/generated", nil, AssetMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(asset.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, asset.Path); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, _, err := store.Open(context.Background(), asset.ID); err == nil {
		t.Fatal("expected symlink asset to be rejected")
	}
}

func TestNewFileStoreRejectsSymlinkedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows runners")
	}
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "images")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := NewFileStore(link, 1<<20); err == nil {
		t.Fatal("expected symlinked image-store root to be rejected")
	}

	parentLink := filepath.Join(t.TempDir(), "parent")
	if err := os.Symlink(target, parentLink); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := NewFileStore(filepath.Join(parentLink, "images"), 1<<20); err == nil {
		t.Fatal("expected symlinked image-store parent to be rejected")
	}
}

func TestOpenPreflightsLocalFileSizeBeforeStreaming(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileStore(root, 4)
	if err != nil {
		t.Fatal(err)
	}
	const id = "0123456789abcdef0123456789abcdef"
	path := filepath.Join(root, id+".bin")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata := assetDiskMetadata{ID: id, Name: "asset.bin", MimeType: "application/octet-stream", Bytes: 10, Path: path}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, id+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Open(context.Background(), id); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected local size preflight rejection, got %v", err)
	}

	// A file within the limit but with stale metadata is also rejected; callers
	// must not trust renderer-controlled byte counts.
	if err := os.WriteFile(path, []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata.Bytes = 99
	raw, _ = json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(root, id+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Open(context.Background(), id); err == nil || !strings.Contains(err.Error(), "metadata size") {
		t.Fatalf("expected metadata size mismatch rejection, got %v", err)
	}
}

func imageResponseClient(body []byte, contentType string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{contentType}},
			Body:          io.NopCloser(bytes.NewReader(body)),
			Request:       request,
			ContentLength: int64(len(body)),
		}, nil
	})}
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	return testPNGBytes(width, height)
}

func testPNGBytes(width, height int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 0xff, A: 0xff})
	var buffer bytes.Buffer
	_ = png.Encode(&buffer, img)
	data := buffer.Bytes()
	if width == 1 && height == 1 {
		return append([]byte(nil), data...)
	}
	// Keep a tiny valid PNG payload while changing only IHDR dimensions. Decode
	// Config reads the header and therefore exercises the bomb guard without
	// allocating width*height pixels.
	if len(data) < 33 {
		return data
	}
	binary.BigEndian.PutUint32(data[16:20], uint32(width))
	binary.BigEndian.PutUint32(data[20:24], uint32(height))
	crc := crc32.ChecksumIEEE(data[12:29])
	binary.BigEndian.PutUint32(data[29:33], crc)
	return data
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
