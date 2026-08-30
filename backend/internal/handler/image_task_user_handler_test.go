package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type userImageHandlerStore struct {
	tasks map[string]*service.ImageTaskRecord
}

func (s *userImageHandlerStore) Save(context.Context, *service.ImageTaskRecord, time.Duration) error {
	return nil
}

func (s *userImageHandlerStore) Get(_ context.Context, id string) (*service.ImageTaskRecord, error) {
	if task := s.tasks[id]; task != nil {
		copy := *task
		return &copy, nil
	}
	return nil, service.ErrImageTaskNotFound
}

func (s *userImageHandlerStore) Transition(context.Context, string, string, *service.ImageTaskRecord, time.Duration) (bool, error) {
	return false, nil
}

func (s *userImageHandlerStore) ListPending(context.Context, int) ([]*service.ImageTaskRecord, error) {
	return nil, nil
}

func (s *userImageHandlerStore) ListUser(_ context.Context, query service.ImageTaskUserQuery) (*service.ImageTaskUserPage, error) {
	page := &service.ImageTaskUserPage{}
	for _, task := range s.tasks {
		if task != nil && task.UserID == query.UserID {
			copy := *task
			page.Tasks = append(page.Tasks, &copy)
		}
	}
	return page, nil
}

func (s *userImageHandlerStore) GetUser(_ context.Context, userID int64, id string) (*service.ImageTaskRecord, error) {
	task := s.tasks[id]
	if task == nil || task.UserID != userID {
		return nil, service.ErrImageTaskNotFound
	}
	copy := *task
	return &copy, nil
}

func (s *userImageHandlerStore) DeleteUser(_ context.Context, userID int64, id string) error {
	task := s.tasks[id]
	if task == nil || task.UserID != userID {
		return service.ErrImageTaskNotFound
	}
	if task.Status == service.ImageTaskStatusProcessing {
		return service.ErrImageTaskDeleteNotReady
	}
	delete(s.tasks, id)
	return nil
}

func userImageHandlerContext(method, path string, userID int64) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, nil)
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	context.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: userID})
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for index, part := range parts {
		if part == "image-tasks" && index+1 < len(parts) {
			context.Params = append(context.Params, gin.Param{Key: "task_id", Value: parts[index+1]})
			if index+3 < len(parts) && parts[index+2] == "assets" {
				context.Params = append(context.Params, gin.Param{Key: "index", Value: parts[index+3]})
			}
			break
		}
	}
	return context, recorder
}

func TestAsyncImageHandlerUserHistoryDoesNotCrossUsers(t *testing.T) {
	store := &userImageHandlerStore{tasks: map[string]*service.ImageTaskRecord{
		"imgtask_owned": {ID: "imgtask_owned", UserID: 7, APIKeyID: 11, Status: service.ImageTaskStatusCompleted, Result: json.RawMessage(`{"data":[{"url":"https://cdn.example.test/a.png"}]}`), ExpiresAt: time.Now().Add(time.Hour).Unix()},
		"imgtask_other": {ID: "imgtask_other", UserID: 8, APIKeyID: 22, Status: service.ImageTaskStatusCompleted, Result: json.RawMessage(`{"data":[{"url":"https://cdn.example.test/b.png"}]}`), ExpiresAt: time.Now().Add(time.Hour).Unix()},
	}}
	h := NewAsyncImageHandler(service.NewImageTaskService(store), nil)

	c, recorder := userImageHandlerContext(http.MethodGet, "/api/v1/user/image-tasks/imgtask_owned", 7)
	h.GetUser(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "api_key_id")
	require.Contains(t, recorder.Body.String(), "a.png")

	c, recorder = userImageHandlerContext(http.MethodGet, "/api/v1/user/image-tasks/imgtask_other", 7)
	h.GetUser(c)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "b.png")
}

func TestAsyncImageHandlerUserAssetReturnsNoStoreFreshLegacyURL(t *testing.T) {
	store := &userImageHandlerStore{tasks: map[string]*service.ImageTaskRecord{
		"imgtask_asset": {ID: "imgtask_asset", UserID: 7, Status: service.ImageTaskStatusCompleted, Result: json.RawMessage(`{"data":[{"url":"https://cdn.example.test/a.png"}]}`), ExpiresAt: time.Now().Add(time.Hour).Unix()},
	}}
	h := NewAsyncImageHandler(service.NewImageTaskService(store), nil)
	c, recorder := userImageHandlerContext(http.MethodGet, "/api/v1/user/image-tasks/imgtask_asset/assets/0", 7)
	h.GetUserAsset(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	require.Contains(t, recorder.Body.String(), "a.png")

	c, recorder = userImageHandlerContext(http.MethodGet, "/api/v1/user/image-tasks/imgtask_asset/assets/1", 7)
	h.GetUserAsset(c)
	require.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestAsyncImageHandlerUserHistoryStripsLegacyInlineBase64(t *testing.T) {
	const secretInlineImage = "c2Vuc2l0aXZlLWltYWdlLWJ5dGVz"
	store := &userImageHandlerStore{tasks: map[string]*service.ImageTaskRecord{
		"imgtask_legacy": {
			ID:        "imgtask_legacy",
			UserID:    7,
			Status:    service.ImageTaskStatusCompleted,
			Result:    json.RawMessage(`{"data":[{"b64_json":"` + secretInlineImage + `","url":"https://cdn.example.test/a.png"}]}`),
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		},
	}}
	h := NewAsyncImageHandler(service.NewImageTaskService(store), nil)
	c, recorder := userImageHandlerContext(http.MethodGet, "/api/v1/user/image-tasks/imgtask_legacy", 7)
	h.GetUser(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotContains(t, recorder.Body.String(), secretInlineImage)
	require.Contains(t, recorder.Body.String(), "https://cdn.example.test/a.png")
}

func TestAsyncImageHandlerUserDeleteRequiresOwnershipAndRemovesTerminalTask(t *testing.T) {
	store := &userImageHandlerStore{tasks: map[string]*service.ImageTaskRecord{
		"imgtask_done":       {ID: "imgtask_done", UserID: 7, Status: service.ImageTaskStatusCompleted},
		"imgtask_processing": {ID: "imgtask_processing", UserID: 7, Status: service.ImageTaskStatusProcessing},
	}}
	h := NewAsyncImageHandler(service.NewImageTaskService(store), nil)

	c, recorder := userImageHandlerContext(http.MethodDelete, "/api/v1/user/image-tasks/imgtask_done", 7)
	h.DeleteUser(c)
	c.Writer.WriteHeaderNow()
	require.Equal(t, http.StatusNoContent, recorder.Code)
	_, exists := store.tasks["imgtask_done"]
	require.False(t, exists)

	c, recorder = userImageHandlerContext(http.MethodDelete, "/api/v1/user/image-tasks/imgtask_processing", 7)
	h.DeleteUser(c)
	require.Equal(t, http.StatusConflict, recorder.Code)

	c, recorder = userImageHandlerContext(http.MethodDelete, "/api/v1/user/image-tasks/imgtask_processing", 8)
	h.DeleteUser(c)
	require.Equal(t, http.StatusNotFound, recorder.Code)
}
