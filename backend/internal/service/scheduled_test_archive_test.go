//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type scheduledPlanRepoArchiveStub struct {
	created *ScheduledTestPlan
	updated *ScheduledTestPlan
}

func (r *scheduledPlanRepoArchiveStub) Create(_ context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	r.created = plan
	return plan, nil
}

func (r *scheduledPlanRepoArchiveStub) GetByID(_ context.Context, _ int64) (*ScheduledTestPlan, error) {
	return nil, nil
}

func (r *scheduledPlanRepoArchiveStub) ListByAccountID(_ context.Context, _ int64) ([]*ScheduledTestPlan, error) {
	return nil, nil
}

func (r *scheduledPlanRepoArchiveStub) ListDue(_ context.Context, _ time.Time) ([]*ScheduledTestPlan, error) {
	return nil, nil
}

func (r *scheduledPlanRepoArchiveStub) Update(_ context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	copied := *plan
	r.updated = &copied
	return plan, nil
}

func (r *scheduledPlanRepoArchiveStub) Delete(_ context.Context, _ int64) error {
	return nil
}

func (r *scheduledPlanRepoArchiveStub) UpdateAfterRun(_ context.Context, _ int64, _ time.Time, _ time.Time) error {
	return nil
}

type scheduledResultRepoArchiveStub struct{}

func (r scheduledResultRepoArchiveStub) Create(_ context.Context, result *ScheduledTestResult) (*ScheduledTestResult, error) {
	return result, nil
}

func (r scheduledResultRepoArchiveStub) ListByPlanID(_ context.Context, _ int64, _ int) ([]*ScheduledTestResult, error) {
	return nil, nil
}

func (r scheduledResultRepoArchiveStub) PruneOldResults(_ context.Context, _ int64, _ int) error {
	return nil
}

func TestScheduledTestService_CreatePlanRejectsArchivedAccount(t *testing.T) {
	now := time.Now()
	accountRepo := &accountRepoStubForBulkUpdate{
		getByIDAccounts: map[int64]*Account{
			1: {ID: 1, ArchivedAt: &now},
		},
	}
	svc := NewScheduledTestService(&scheduledPlanRepoArchiveStub{}, scheduledResultRepoArchiveStub{}, accountRepo)

	_, err := svc.CreatePlan(context.Background(), &ScheduledTestPlan{
		AccountID:      1,
		CronExpression: "* * * * *",
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "account is archived")
}

func TestScheduledTestService_UpdateEnabledPlanRejectsArchivedAccount(t *testing.T) {
	now := time.Now()
	accountRepo := &accountRepoStubForBulkUpdate{
		getByIDAccounts: map[int64]*Account{
			1: {ID: 1, ArchivedAt: &now},
		},
	}
	svc := NewScheduledTestService(&scheduledPlanRepoArchiveStub{}, scheduledResultRepoArchiveStub{}, accountRepo)

	_, err := svc.UpdatePlan(context.Background(), &ScheduledTestPlan{
		ID:             10,
		AccountID:      1,
		CronExpression: "* * * * *",
		Enabled:        true,
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "account is archived")
}

func TestScheduledTestRunner_DisablesArchivedAccountPlan(t *testing.T) {
	now := time.Now()
	accountRepo := &accountRepoStubForBulkUpdate{
		getByIDAccounts: map[int64]*Account{
			1: {ID: 1, ArchivedAt: &now},
		},
	}
	planRepo := &scheduledPlanRepoArchiveStub{}
	runner := NewScheduledTestRunnerService(planRepo, nil, nil, accountRepo, nil, nil)

	nextRun := time.Now()
	runner.runOnePlan(context.Background(), &ScheduledTestPlan{
		ID:             10,
		AccountID:      1,
		CronExpression: "* * * * *",
		Enabled:        true,
		NextRunAt:      &nextRun,
	})

	require.NotNil(t, planRepo.updated)
	require.False(t, planRepo.updated.Enabled)
	require.Nil(t, planRepo.updated.NextRunAt)
}
