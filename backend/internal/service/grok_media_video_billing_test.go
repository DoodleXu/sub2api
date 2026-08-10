package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type grokVideoTaskRepoStub struct {
	tasks   map[string]*GrokVideoTask
	claimed map[string]bool
	billed  map[string]bool
}

type grokVideoLookupAccountRepoStub struct {
	AccountRepository
	accounts map[int64]*Account
}

func (s *grokVideoLookupAccountRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	account := s.accounts[id]
	if account == nil {
		return nil, ErrAccountNotFound
	}
	copy := *account
	return &copy, nil
}

func (s *grokVideoTaskRepoStub) Upsert(_ context.Context, task *GrokVideoTask) error {
	if s.tasks == nil {
		s.tasks = make(map[string]*GrokVideoTask)
	}
	copy := *task
	s.tasks[task.RequestID] = &copy
	return nil
}

func (s *grokVideoTaskRepoStub) GetByOwner(_ context.Context, requestID string, userID, apiKeyID int64) (*GrokVideoTask, error) {
	task := s.tasks[requestID]
	if task == nil || task.UserID != userID || task.APIKeyID != apiKeyID {
		return nil, ErrGrokVideoTaskNotFound
	}
	copy := *task
	return &copy, nil
}

func (s *grokVideoTaskRepoStub) ClaimBilling(_ context.Context, requestID string, userID, apiKeyID int64) (bool, error) {
	if _, err := s.GetByOwner(context.Background(), requestID, userID, apiKeyID); err != nil {
		return false, err
	}
	if s.claimed == nil {
		s.claimed = make(map[string]bool)
	}
	if s.claimed[requestID] || s.billed[requestID] {
		return false, nil
	}
	s.claimed[requestID] = true
	return true, nil
}

func (s *grokVideoTaskRepoStub) ReleaseBilling(_ context.Context, requestID string, userID, apiKeyID int64) error {
	if _, err := s.GetByOwner(context.Background(), requestID, userID, apiKeyID); err != nil {
		return err
	}
	delete(s.claimed, requestID)
	return nil
}

func (s *grokVideoTaskRepoStub) MarkBilled(_ context.Context, requestID string, userID, apiKeyID int64) error {
	if _, err := s.GetByOwner(context.Background(), requestID, userID, apiKeyID); err != nil {
		return err
	}
	if s.billed == nil {
		s.billed = make(map[string]bool)
	}
	s.billed[requestID] = true
	return nil
}

func TestGrokVideoE2EDurationFromCreatedAt(t *testing.T) {
	t.Parallel()
	created := time.Now().UTC().Add(-45 * time.Second)
	d := GrokVideoE2EDuration(created.Format(time.RFC3339Nano), time.Now().UTC())
	require.GreaterOrEqual(t, d, 44*time.Second)
	require.LessOrEqual(t, d, 47*time.Second)

	require.Equal(t, time.Duration(0), GrokVideoE2EDuration("", time.Now()))
	require.Equal(t, time.Duration(0), GrokVideoE2EDuration("not-a-time", time.Now()))
	// Future CreatedAt clamps to zero (clock skew).
	require.Equal(t, time.Duration(0), GrokVideoE2EDuration(time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), time.Now()))
}

func TestGrokVideoPendingCreatedAtStampOnStoreShape(t *testing.T) {
	t.Parallel()
	// GrokVideoPendingCreatedAtNow must be parseable by GrokVideoE2EDuration.
	stamp := GrokVideoPendingCreatedAtNow()
	require.NotEmpty(t, stamp)
	d := GrokVideoE2EDuration(stamp, time.Now().UTC().Add(2*time.Second))
	require.GreaterOrEqual(t, d, time.Second)
	require.LessOrEqual(t, d, 3*time.Second)
}

func TestGrokVideoTaskDurableStoreOwnsRoutingSnapshotAndBillingClaim(t *testing.T) {
	repo := &grokVideoTaskRepoStub{}
	svc := &OpenAIGatewayService{grokVideoTaskRepo: repo}
	groupID := int64(7)
	pending := GrokVideoPendingBilling{
		Model:                "grok-imagine-video-1.5",
		BillingModel:         "grok-imagine-video-1.5",
		VideoResolution:      VideoBillingResolution1080P,
		VideoDurationSeconds: 10,
		CreatedAt:            GrokVideoPendingCreatedAtNow(),
	}
	require.NoError(t, svc.RegisterGrokMediaVideoTask(context.Background(), &groupID, "video-1", 11, 12, 13, pending))

	accountID, err := svc.ResolveGrokMediaVideoRequestAccount(context.Background(), &groupID, "video-1", 11, 12)
	require.NoError(t, err)
	require.Equal(t, int64(13), accountID)

	loaded, err := svc.LoadGrokVideoPendingBilling(context.Background(), "video-1", 11, 12)
	require.NoError(t, err)
	require.Equal(t, VideoBillingResolution1080P, loaded.VideoResolution)
	require.Equal(t, 10, loaded.VideoDurationSeconds)

	claimed, err := svc.ClaimGrokVideoBilling(context.Background(), "video-1", 11, 12)
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = svc.ClaimGrokVideoBilling(context.Background(), "video-1", 11, 12)
	require.NoError(t, err)
	require.False(t, claimed)
	require.NoError(t, svc.ReleaseGrokVideoBilling(context.Background(), "video-1", 11, 12))
	claimed, err = svc.ClaimGrokVideoBilling(context.Background(), "video-1", 11, 12)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, svc.MarkGrokVideoBillingCompleted(context.Background(), "video-1", 11, 12))
	claimed, err = svc.ClaimGrokVideoBilling(context.Background(), "video-1", 11, 12)
	require.NoError(t, err)
	require.False(t, claimed)
}

func TestSelectGrokMediaVideoLookupAccountKeepsDurableAccountBindingWithoutCache(t *testing.T) {
	groupID := int64(77)
	bound := &Account{
		ID:          701,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		GroupIDs:    []int64{groupID},
	}
	other := &Account{
		ID:          702,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		GroupIDs:    []int64{groupID},
		Priority:    100,
	}
	svc := &OpenAIGatewayService{
		accountRepo: &grokVideoLookupAccountRepoStub{accounts: map[int64]*Account{
			bound.ID: bound,
			other.ID: other,
		}},
		// Deliberately no cache: this models Redis loss after a video create.
	}

	selection, decision, err := svc.SelectGrokMediaVideoLookupAccount(context.Background(), &groupID, bound.ID, "")

	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, bound.ID, selection.Account.ID)
	require.Equal(t, "grok_video_task_binding", decision.Layer)
	require.Equal(t, bound.ID, decision.SelectedAccountID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}

	_, _, err = svc.SelectGrokMediaVideoLookupAccount(context.Background(), &groupID, 999, "")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrGrokVideoTaskAccountUnavailable) || errors.Is(err, ErrAccountNotFound))
}

func TestIsGrokVideoStatusBillable(t *testing.T) {
	t.Parallel()
	// Official success: status=done + video.url
	require.True(t, IsGrokVideoStatusBillable([]byte(`{
		"status":"done",
		"model":"grok-imagine-video-1.5",
		"video":{"url":"https://vidgen.x.ai/x.mp4","duration":8,"respect_moderation":true}
	}`)))

	// Official non-success states
	require.False(t, IsGrokVideoStatusBillable(nil))
	require.False(t, IsGrokVideoStatusBillable([]byte(`{"status":"pending"}`)))
	require.False(t, IsGrokVideoStatusBillable([]byte(`{"status":"expired"}`)))
	require.False(t, IsGrokVideoStatusBillable([]byte(`{"status":"failed"}`)))
	// done without video.url is not billable
	require.False(t, IsGrokVideoStatusBillable([]byte(`{"status":"done"}`)))
	// URL alone (legacy/non-official shapes) is not enough
	require.False(t, IsGrokVideoStatusBillable([]byte(`{"url":"https://example.com/v.mp4"}`)))
	require.False(t, IsGrokVideoStatusBillable([]byte(`{"download_url":"/v1/videos/task/content"}`)))
	// "completed" is not the official enum value
	require.False(t, IsGrokVideoStatusBillable([]byte(`{"status":"completed","video":{"url":"https://vidgen.x.ai/x.mp4"}}`)))
}

func TestExtractGrokVideoBillingFromStatusBodyPrefersUpstreamParams(t *testing.T) {
	t.Parallel()
	pending := &GrokVideoPendingBilling{
		Model:                "pending-model",
		BillingModel:         "pending-billing",
		UpstreamModel:        "pending-upstream",
		VideoResolution:      VideoBillingResolution720P,
		VideoDurationSeconds: 8,
	}
	// Official completed body from docs.x.ai Video Generation.
	body := []byte(`{
		"status":"done",
		"model":"grok-imagine-video-1.5",
		"video":{"url":"https://vidgen.x.ai/signed.mp4","duration":12,"respect_moderation":true}
	}`)
	result := ExtractGrokVideoBillingFromStatusBody(body, pending, "req-1")
	require.NotNil(t, result)
	require.Equal(t, 1, result.VideoCount)
	require.Equal(t, "grok-imagine-video-1.5", result.Model)
	// Resolution is not in official status response — use create-time request.
	require.Equal(t, VideoBillingResolution720P, result.VideoResolution)
	// Duration prefers official video.duration.
	require.Equal(t, 12, result.VideoDurationSeconds)
}

func TestExtractGrokVideoBillingFromStatusBodyFallsBackToPending(t *testing.T) {
	t.Parallel()
	pending := &GrokVideoPendingBilling{
		Model:                "create-model",
		BillingModel:         "create-billing",
		UpstreamModel:        "create-upstream",
		VideoResolution:      VideoBillingResolution1080P,
		VideoDurationSeconds: 10,
	}
	// done + video.url, but no model/duration in body.
	body := []byte(`{"status":"done","video":{"url":"https://vidgen.x.ai/signed.mp4"}}`)
	result := ExtractGrokVideoBillingFromStatusBody(body, pending, "req-2")
	require.NotNil(t, result)
	require.Equal(t, "create-billing", result.BillingModel)
	require.Equal(t, "create-upstream", result.UpstreamModel)
	require.Equal(t, VideoBillingResolution1080P, result.VideoResolution)
	require.Equal(t, 10, result.VideoDurationSeconds)
}

func TestExtractGrokVideoBillingRejectsNonDoneStatus(t *testing.T) {
	t.Parallel()
	pending := &GrokVideoPendingBilling{Model: "m", VideoDurationSeconds: 8, VideoResolution: "720p"}
	require.Nil(t, ExtractGrokVideoBillingFromStatusBody(
		[]byte(`{"status":"pending","video":{"url":"https://vidgen.x.ai/x.mp4","duration":8}}`),
		pending, "req",
	))
	require.Nil(t, ExtractGrokVideoBillingFromStatusBody(
		[]byte(`{"status":"completed","video":{"url":"https://vidgen.x.ai/x.mp4","duration":8}}`),
		pending, "req",
	))
}

func TestGrokMediaUsageFromResponseVideoCreateDoesNotBill(t *testing.T) {
	t.Parallel()
	info := GrokMediaRequestInfo{Model: "grok-imagine-video", Resolution: "720p", DurationSeconds: 10}
	meta := grokMediaUsageFromResponse(GrokMediaEndpointVideosGenerations, info, []byte(`{"request_id":"v1"}`))
	require.Equal(t, "v1", meta.ResponseID)
	require.Equal(t, 0, meta.VideoCount)
	require.Equal(t, 10, meta.VideoDurationSeconds)
	require.Equal(t, VideoBillingResolution720P, meta.VideoResolution)
}

func TestGrokMediaUsageFromResponseVideoStatusBillsOnOfficialDone(t *testing.T) {
	t.Parallel()
	meta := grokMediaUsageFromResponse(
		GrokMediaEndpointVideoStatus,
		GrokMediaRequestInfo{},
		[]byte(`{"status":"done","model":"grok-imagine-video-1.5","video":{"url":"https://vidgen.x.ai/a.mp4","duration":9}}`),
	)
	require.Equal(t, 1, meta.VideoCount)
	require.Equal(t, 9, meta.VideoDurationSeconds)
	require.Equal(t, "grok-imagine-video-1.5", meta.Model)

	// Official non-done must not set billable units.
	pendingOnly := grokMediaUsageFromResponse(
		GrokMediaEndpointVideoStatus,
		GrokMediaRequestInfo{},
		[]byte(`{"status":"pending"}`),
	)
	require.Equal(t, 0, pendingOnly.VideoCount)

	// completed is not official done.
	completed := grokMediaUsageFromResponse(
		GrokMediaEndpointVideoStatus,
		GrokMediaRequestInfo{},
		[]byte(`{"status":"completed","video":{"url":"https://vidgen.x.ai/a.mp4","duration":9}}`),
	)
	require.Equal(t, 0, completed.VideoCount)
}
