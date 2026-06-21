package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

func TestDecodeArchivedImageAcceptsPaddedBase64(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "padded", raw: "aW1hZ2UtMQ=="},
		{name: "unpadded", raw: "aW1hZ2UtMQ"},
		{name: "data-url", raw: "data:image/png;base64,aW1hZ2UtMQ=="},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, mimeType, ext, err := decodeArchivedImage(ArchivedImageInput{B64JSON: tt.raw})

			require.NoError(t, err)
			require.Equal(t, []byte("image-1"), b)
			require.Equal(t, "image/png", mimeType)
			require.Equal(t, ".png", ext)
		})
	}
}

func TestArchiveImageBytesSyncRespectsDisabledStorage(t *testing.T) {
	settingRepo := &imageArchiveSettingRepoStub{
		values: map[string]string{
			imageArchiveStorageSettingKey: `{"enabled":false,"type":"local","local_dir":"./data/image-archive"}`,
		},
	}
	repo := &imageArchiveRepoStub{}
	storage := &imageArchiveStorageStub{}
	svc := NewImageGenerationArchiveService(repo, settingRepo, nil, nil)
	svc.SetStorage(storage)

	err := svc.ArchiveImageBytesSync(context.Background(), &ImageGenerationRecord{ID: 42}, []ArchivedImageBytesInput{
		{Index: 0, Bytes: []byte("image"), MimeType: "image/png", Extension: ".png"},
	})

	require.ErrorIs(t, err, ErrImageArchiveDisabled)
	require.Zero(t, storage.saveCalls)
	require.Empty(t, repo.assets)
}

type imageArchiveRepoStub struct {
	records           []*ImageGenerationRecord
	assets            []*ImageGenerationAsset
	storageType       string
	deleteRecordCalls int
}

func (r *imageArchiveRepoStub) CreateRecord(_ context.Context, record *ImageGenerationRecord) error {
	record.ID = int64(len(r.records) + 1)
	r.records = append(r.records, record)
	return nil
}

func (r *imageArchiveRepoStub) UpdateRecord(_ context.Context, record *ImageGenerationRecord) error {
	r.records = append(r.records, record)
	return nil
}

func (r *imageArchiveRepoStub) GetRecordByID(_ context.Context, id int64) (*ImageGenerationRecord, []*ImageGenerationAsset, error) {
	return &ImageGenerationRecord{ID: id}, r.assets, nil
}

func (r *imageArchiveRepoStub) ListRecords(context.Context, ImageGenerationRecordListParams) ([]*ImageGenerationRecord, *ImageGenerationRecordListResult, error) {
	return r.records, &ImageGenerationRecordListResult{}, nil
}

func (r *imageArchiveRepoStub) ListAllArchiveStorageRefs(context.Context) (*ImageGenerationArchiveClearResult, error) {
	refs := make([]ImageGenerationAssetStorageRef, 0, len(r.assets))
	for _, asset := range r.assets {
		refs = append(refs, ImageGenerationAssetStorageRef{
			ID:          asset.ID,
			StorageKey:  asset.StorageKey,
			StorageType: defaultString(r.storageType, "local"),
		})
	}
	result := &ImageGenerationArchiveClearResult{
		RecordsDeleted: int64(len(r.records)),
		AssetsDeleted:  int64(len(r.assets)),
		AssetRefs:      refs,
	}
	return result, nil
}

func (r *imageArchiveRepoStub) DeleteAllArchiveRecords(context.Context) (int64, error) {
	r.deleteRecordCalls++
	deleted := int64(len(r.records))
	r.records = nil
	r.assets = nil
	return deleted, nil
}

func (r *imageArchiveRepoStub) ListDailyStats(context.Context, ImageGenerationRecordDailyStatsParams) ([]ImageGenerationDailyStat, error) {
	return nil, nil
}

func (r *imageArchiveRepoStub) GetStorageStats(context.Context) (ImageGenerationStorageStats, error) {
	return ImageGenerationStorageStats{}, nil
}

func (r *imageArchiveRepoStub) CreateAsset(_ context.Context, asset *ImageGenerationAsset) error {
	r.assets = append(r.assets, asset)
	return nil
}

func (r *imageArchiveRepoStub) GetAssetByID(_ context.Context, id int64) (*ImageGenerationAsset, *ImageGenerationRecord, error) {
	return &ImageGenerationAsset{ID: id, RecordID: 1}, &ImageGenerationRecord{ID: 1}, nil
}

func (r *imageArchiveRepoStub) ListAssetsByRecordID(context.Context, int64) ([]*ImageGenerationAsset, error) {
	return r.assets, nil
}

func (r *imageArchiveRepoStub) CreateWebConsoleTask(_ context.Context, task *WebConsoleImageTask) error {
	task.ID = 1
	return nil
}

func (r *imageArchiveRepoStub) ClaimWebConsoleTask(context.Context, int64, time.Time) (*WebConsoleImageTask, bool, error) {
	return nil, false, nil
}

func (r *imageArchiveRepoStub) GetWebConsoleTaskByID(context.Context, int64) (*WebConsoleImageTask, error) {
	return nil, ErrWebConsoleImageTaskNotFound
}

func (r *imageArchiveRepoStub) ListWebConsoleTasksByUserID(context.Context, int64, pagination.PaginationParams) ([]*WebConsoleImageTask, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

func (r *imageArchiveRepoStub) MarkWebConsoleTasksUserDeletedBySessionID(context.Context, int64, string) (int64, error) {
	return 0, nil
}

func (r *imageArchiveRepoStub) UpdateWebConsoleTask(context.Context, *WebConsoleImageTask) error {
	return nil
}

func (r *imageArchiveRepoStub) CountDailyByDate(context.Context, time.Time) (int64, error) {
	return 0, nil
}

type imageArchiveSettingRepoStub struct {
	values map[string]string
}

func (r *imageArchiveSettingRepoStub) Get(_ context.Context, key string) (*Setting, error) {
	value, err := r.GetValue(context.Background(), key)
	if err != nil {
		return nil, err
	}
	return &Setting{Key: key, Value: value}, nil
}

func (r *imageArchiveSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if r == nil || r.values == nil {
		return "", ErrSettingNotFound
	}
	value, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (r *imageArchiveSettingRepoStub) Set(_ context.Context, key, value string) error {
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}

func (r *imageArchiveSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (r *imageArchiveSettingRepoStub) SetMultiple(_ context.Context, settings map[string]string) error {
	for key, value := range settings {
		if err := r.Set(context.Background(), key, value); err != nil {
			return err
		}
	}
	return nil
}

func (r *imageArchiveSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	return r.values, nil
}

func (r *imageArchiveSettingRepoStub) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

type imageArchiveStorageStub struct {
	saveCalls int
	deleteErr error
	deleted   []string
}

func (s *imageArchiveStorageStub) Save(context.Context, []byte, StoredImageMeta) (*StoredImage, error) {
	s.saveCalls++
	return &StoredImage{StorageType: "local", StorageKey: "image.png", Bytes: 5, SHA256: strings.Repeat("0", 64), MimeType: "image/png", Extension: ".png"}, nil
}

func (s *imageArchiveStorageStub) ResolveURL(context.Context, *StoredImage, bool) string {
	return ""
}

func (s *imageArchiveStorageStub) Open(context.Context, string) (io.ReadCloser, string, error) {
	return io.NopCloser(strings.NewReader("")), "image/png", nil
}

func (s *imageArchiveStorageStub) Delete(_ context.Context, storageKey string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deleted = append(s.deleted, storageKey)
	return nil
}

func TestClearAllArchivesDeletesRecordsAfterStorageCleanupSucceeds(t *testing.T) {
	repo := &imageArchiveRepoStub{
		records:     []*ImageGenerationRecord{{ID: 1}, {ID: 2}},
		storageType: "local",
		assets: []*ImageGenerationAsset{
			{ID: 7, StorageKey: "2026/06/image-1.png"},
			{ID: 8, StorageKey: "2026/06/image-2.png"},
		},
	}
	storage := &imageArchiveStorageStub{}
	svc := NewImageGenerationArchiveService(repo, &imageArchiveSettingRepoStub{}, nil, nil)
	svc.SetStorage(storage)

	result, err := svc.ClearAllArchives(context.Background())

	require.NoError(t, err)
	require.Equal(t, int64(2), result.RecordsDeleted)
	require.Equal(t, int64(2), result.AssetsDeleted)
	require.Zero(t, result.StorageDeleteFailures)
	require.Equal(t, []string{"2026/06/image-1.png", "2026/06/image-2.png"}, storage.deleted)
	require.Equal(t, 1, repo.deleteRecordCalls)
	require.Empty(t, repo.records)
	require.Empty(t, repo.assets)
}

func TestClearAllArchivesKeepsRecordsWhenStorageCleanupFails(t *testing.T) {
	repo := &imageArchiveRepoStub{
		records:     []*ImageGenerationRecord{{ID: 1}},
		storageType: "local",
		assets:      []*ImageGenerationAsset{{ID: 7, StorageKey: "2026/06/image.png"}},
	}
	storage := &imageArchiveStorageStub{deleteErr: errors.New("storage unavailable")}
	svc := NewImageGenerationArchiveService(repo, &imageArchiveSettingRepoStub{}, nil, nil)
	svc.SetStorage(storage)

	result, err := svc.ClearAllArchives(context.Background())

	require.NoError(t, err)
	require.Zero(t, result.RecordsDeleted)
	require.Zero(t, result.AssetsDeleted)
	require.Equal(t, int64(1), result.StorageDeleteFailures)
	require.Zero(t, repo.deleteRecordCalls)
	require.Len(t, repo.records, 1)
	require.Len(t, repo.assets, 1)
}
