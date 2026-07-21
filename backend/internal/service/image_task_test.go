package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

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

	svc.reconcilePendingObjects()

	require.Equal(t, ImageTaskStatusFailed, store.task.Status)
	require.Equal(t, http.StatusGatewayTimeout, store.task.HTTPStatus)
	require.Contains(t, string(store.task.Error), "timed out")
	require.NotNil(t, store.task.CompletedAt)
	require.Empty(t, store.task.PendingObjectKeys)
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
