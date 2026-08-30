package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type imageTaskMemoryStore struct {
	task                     *ImageTaskRecord
	ttl                      time.Duration
	saveErr                  error
	getErr                   error
	forceTransitionMiss      bool
	transitionErrNoCommit    error
	transitionErrAfterCommit error
	respectContext           bool
}

func (s *imageTaskMemoryStore) Save(_ context.Context, task *ImageTaskRecord, ttl time.Duration) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	copy := *task
	copy.PendingObjectKeys = append([]string(nil), task.PendingObjectKeys...)
	s.task = &copy
	s.ttl = ttl
	return nil
}

func (s *imageTaskMemoryStore) Get(ctx context.Context, _ string) (*ImageTaskRecord, error) {
	if s.respectContext && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.task == nil {
		return nil, ErrImageTaskNotFound
	}
	copy := *s.task
	copy.PendingObjectKeys = append([]string(nil), s.task.PendingObjectKeys...)
	return &copy, nil
}

func (s *imageTaskMemoryStore) ListPending(context.Context, int) ([]*ImageTaskRecord, error) {
	if s.task == nil || (s.task.Status != ImageTaskStatusProcessing &&
		(s.task.Status != ImageTaskStatusFailed || len(s.task.PendingObjectKeys) == 0)) {
		return nil, nil
	}
	copy := *s.task
	copy.PendingObjectKeys = append([]string(nil), s.task.PendingObjectKeys...)
	if copy.Status != ImageTaskStatusProcessing && copy.Status != ImageTaskStatusFailed {
		return nil, nil
	}
	return []*ImageTaskRecord{&copy}, nil
}

func TestImageTaskServiceReconcilesAbandonedProcessingWithoutObjects(t *testing.T) {
	createdAt := time.Now().Add(-2 * time.Minute).Unix()
	store := &imageTaskMemoryStore{task: &ImageTaskRecord{
		ID: "imgtask_abandoned", Status: ImageTaskStatusProcessing,
		CreatedAt: createdAt, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}}
	svc := NewImageTaskServiceWithOptions(store, time.Hour, time.Minute)
	svc.uploader = &ImageResultUploader{}
	svc.taskUploaders.Store(store.task.ID, svc.uploader)

	svc.reconcilePendingObjects()

	require.Equal(t, ImageTaskStatusFailed, store.task.Status)
	require.Equal(t, http.StatusGatewayTimeout, store.task.HTTPStatus)
	require.Contains(t, string(store.task.Error), "timed out")
	require.NotNil(t, store.task.CompletedAt)
	require.Empty(t, store.task.PendingObjectKeys)
	_, snapshotExists := svc.taskUploaders.Load(store.task.ID)
	require.False(t, snapshotExists)
}

func (s *imageTaskMemoryStore) Transition(ctx context.Context, _ string, expectedStatus string, task *ImageTaskRecord, ttl time.Duration) (bool, error) {
	if s.respectContext && ctx.Err() != nil {
		return false, ctx.Err()
	}
	if s.saveErr != nil {
		return false, s.saveErr
	}
	if s.task == nil {
		return false, ErrImageTaskNotFound
	}
	if s.forceTransitionMiss {
		return false, nil
	}
	if task.Status == ImageTaskStatusCompleted && s.transitionErrNoCommit != nil {
		return false, s.transitionErrNoCommit
	}
	if s.task.Status != expectedStatus {
		return false, nil
	}
	copy := *task
	copy.PendingObjectKeys = append([]string(nil), task.PendingObjectKeys...)
	s.task = &copy
	s.ttl = ttl
	if task.Status == ImageTaskStatusCompleted && s.transitionErrAfterCommit != nil {
		return false, s.transitionErrAfterCommit
	}
	return true, nil
}

func TestImageTaskServiceLifecycleAndOwnership(t *testing.T) {
	store := &imageTaskMemoryStore{}
	svc := NewImageTaskServiceWithOptions(store, time.Hour, 10*time.Minute)
	owner := ImageTaskOwner{UserID: 7, APIKeyID: 9}

	created, err := svc.Create(context.Background(), owner)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusProcessing, created.Status)
	require.Equal(t, created.ID, created.TaskID)
	require.Equal(t, "image.generation.task", created.Object)
	require.Equal(t, time.Hour, store.ttl)
	require.Equal(t, owner.UserID, store.task.UserID)
	require.Equal(t, owner.APIKeyID, store.task.APIKeyID)

	_, err = svc.Get(context.Background(), ImageTaskOwner{UserID: 7, APIKeyID: 10}, created.ID)
	require.ErrorIs(t, err, ErrImageTaskNotFound)

	result := json.RawMessage(`{"created":123,"data":[{"url":"https://example.test/image.png"}]}`)
	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK, result))

	completed, err := svc.Get(context.Background(), owner, created.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusCompleted, completed.Status)
	require.Equal(t, http.StatusOK, completed.HTTPStatus)
	require.Equal(t, "https://example.test/image.png", completed.ImageURL)
	require.JSONEq(t, string(result), string(completed.Result))
	require.NotNil(t, completed.CompletedAt)
}

func TestImageTaskServicePersistsStorageBindingAtAdmission(t *testing.T) {
	store := &imageTaskMemoryStore{}
	uploader := NewImageResultUploader(&fakeImageStorage{}, "images/", 0, nil)
	uploader.SetBindingID("imgbind_test")
	svc := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)
	t.Cleanup(svc.Close)

	_, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2})
	require.NoError(t, err)
	require.Equal(t, "imgbind_test", store.task.StorageBindingID)
}

func TestImageTaskServiceInvalidResultBecomesFailed(t *testing.T) {
	store := &imageTaskMemoryStore{}
	svc := NewImageTaskServiceWithOptions(store, time.Hour, time.Minute)
	created, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2})
	require.NoError(t, err)

	require.NoError(t, svc.Complete(context.Background(), created.ID, http.StatusOK, json.RawMessage(`not-json`)))
	got, err := svc.Get(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2}, created.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStatusFailed, got.Status)
	require.Equal(t, http.StatusBadGateway, got.HTTPStatus)
	require.Contains(t, string(got.Error), "non-JSON")
}

func TestImageTaskServicePreservesExistingTerminalState(t *testing.T) {
	store := &imageTaskMemoryStore{task: &ImageTaskRecord{ID: "imgtask_done", Status: ImageTaskStatusCompleted}}
	svc := NewImageTaskServiceWithOptions(store, time.Hour, time.Minute)

	require.NoError(t, svc.Fail(context.Background(), "imgtask_done", http.StatusBadGateway, json.RawMessage(`{"error":{"message":"late failure"}}`)))
	require.Equal(t, ImageTaskStatusCompleted, store.task.Status)
}

func TestImageTaskServiceMapsStoreFailures(t *testing.T) {
	store := &imageTaskMemoryStore{saveErr: errors.New("redis down")}
	svc := NewImageTaskService(store)

	_, err := svc.Create(context.Background(), ImageTaskOwner{UserID: 1, APIKeyID: 2})
	require.ErrorIs(t, err, ErrImageTaskUnavailable)
}

type imageTaskAdminMemoryStore struct {
	imageTaskMemoryStore
	listAdminCalled bool
}

func (s *imageTaskAdminMemoryStore) ListAdmin(context.Context, ImageTaskAdminQuery) (*ImageTaskAdminPage, error) {
	s.listAdminCalled = true
	return &ImageTaskAdminPage{}, nil
}

func TestImageTaskServiceRejectsInvalidAdminCursorAsBadRequest(t *testing.T) {
	store := &imageTaskAdminMemoryStore{}
	svc := NewImageTaskService(store)

	_, err := svc.ListAdmin(context.Background(), ImageTaskAdminQuery{Cursor: "not-a-valid-cursor"})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Equal(t, "INVALID_IMAGE_TASK_CURSOR", infraerrors.Reason(err))
	require.False(t, store.listAdminCalled)
}

type imageTaskUserMemoryStore struct {
	imageTaskMemoryStore
	tasks map[string]*ImageTaskRecord
}

func (s *imageTaskUserMemoryStore) ListUser(_ context.Context, query ImageTaskUserQuery) (*ImageTaskUserPage, error) {
	page := &ImageTaskUserPage{}
	for _, task := range s.tasks {
		if task != nil && task.UserID == query.UserID && (query.Status == "" || query.Status == "all" || task.Status == query.Status) {
			copy := *task
			page.Tasks = append(page.Tasks, &copy)
		}
	}
	return page, nil
}

func (s *imageTaskUserMemoryStore) GetUser(_ context.Context, userID int64, id string) (*ImageTaskRecord, error) {
	task := s.tasks[id]
	if task == nil || task.UserID != userID {
		return nil, ErrImageTaskNotFound
	}
	copy := *task
	return &copy, nil
}

func (s *imageTaskUserMemoryStore) DeleteUser(_ context.Context, userID int64, id string) error {
	task := s.tasks[id]
	if task == nil || task.UserID != userID {
		return ErrImageTaskNotFound
	}
	if task.Status == ImageTaskStatusProcessing {
		return ErrImageTaskDeleteNotReady
	}
	delete(s.tasks, id)
	return nil
}

func TestImageTaskServiceUserHistoryUsesJWTUserOwnership(t *testing.T) {
	store := &imageTaskUserMemoryStore{
		imageTaskMemoryStore: imageTaskMemoryStore{},
		tasks: map[string]*ImageTaskRecord{
			"imgtask_a": {ID: "imgtask_a", UserID: 7, APIKeyID: 11, Status: ImageTaskStatusCompleted},
			"imgtask_b": {ID: "imgtask_b", UserID: 8, APIKeyID: 22, Status: ImageTaskStatusCompleted},
		},
	}
	svc := NewImageTaskService(store)

	page, err := svc.ListUser(context.Background(), ImageTaskUserQuery{UserID: 7, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Tasks, 1)
	require.Equal(t, int64(7), page.Tasks[0].UserID)

	task, err := svc.GetUser(context.Background(), 7, "imgtask_a")
	require.NoError(t, err)
	require.Equal(t, "imgtask_a", task.ID)
	_, err = svc.GetUser(context.Background(), 7, "imgtask_b")
	require.ErrorIs(t, err, ErrImageTaskNotFound)
}

func TestImageTaskServiceResolveResultURLsKeepsTerminalHistoryAndRejectsExpiredProcessing(t *testing.T) {
	storage := &resolvingImageStorage{urls: map[string]string{"images/imgtask_a-0.png": "https://cdn.example.test/fresh.png"}}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	uploader.SetBindingID("binding-a")
	store := &imageTaskUserMemoryStore{tasks: map[string]*ImageTaskRecord{
		"imgtask_a": {ID: "imgtask_a", UserID: 7, Status: ImageTaskStatusCompleted, StorageBindingID: "binding-a", ResultObjectKeys: []string{"images/imgtask_a-0.png"}, ExpiresAt: time.Now().Add(time.Hour).Unix()},
	}}
	svc := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)
	t.Cleanup(svc.Close)
	urls, err := svc.ResolveResultURLs(context.Background(), store.tasks["imgtask_a"])
	require.NoError(t, err)
	require.Equal(t, []string{"https://cdn.example.test/fresh.png"}, urls)

	expired := *store.tasks["imgtask_a"]
	expired.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	urls, err = svc.ResolveResultURLs(context.Background(), &expired)
	require.NoError(t, err)
	require.Equal(t, []string{"https://cdn.example.test/fresh.png"}, urls)

	expired.Status = ImageTaskStatusProcessing
	_, err = svc.ResolveResultURLs(context.Background(), &expired)
	require.ErrorIs(t, err, ErrImageTaskAssetsExpired)
}

func TestImageTaskServiceDeleteUserRemovesTerminalObjectsAndHistory(t *testing.T) {
	storage := &fakeImageStorage{}
	uploader := NewImageResultUploader(storage, "images/", 0, nil)
	uploader.SetBindingID("binding-a")
	store := &imageTaskUserMemoryStore{tasks: map[string]*ImageTaskRecord{
		"imgtask_delete": {
			ID: "imgtask_delete", UserID: 7, Status: ImageTaskStatusCompleted,
			StorageBindingID: "binding-a", ResultObjectKeys: []string{"images/result.png"},
			PendingObjectKeys: []string{"images/pending.png"},
		},
		"imgtask_processing": {ID: "imgtask_processing", UserID: 7, Status: ImageTaskStatusProcessing},
	}}
	svc := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)
	t.Cleanup(svc.Close)

	require.NoError(t, svc.DeleteUser(context.Background(), 7, "imgtask_delete"))
	require.ElementsMatch(t, []string{"images/result.png", "images/pending.png"}, storage.deleted)
	_, ok := store.tasks["imgtask_delete"]
	require.False(t, ok)

	require.ErrorIs(t, svc.DeleteUser(context.Background(), 7, "imgtask_processing"), ErrImageTaskDeleteNotReady)
	require.ErrorIs(t, svc.DeleteUser(context.Background(), 8, "imgtask_processing"), ErrImageTaskNotFound)
}

type resolvingImageStorage struct {
	urls map[string]string
}

func (s *resolvingImageStorage) Save(context.Context, string, string, []byte) (string, error) {
	return "https://cdn.example.test/uploaded.png", nil
}

func (s *resolvingImageStorage) Delete(context.Context, string) error { return nil }

func (s *resolvingImageStorage) ResolveURLs(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value := s.urls[key]; value != "" {
			result[key] = value
		}
	}
	return result, nil
}
