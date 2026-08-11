package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type usageDeleteRepoStub struct {
	UsageLogRepository
	usageLog *UsageLog
	deleted  int64
}

func (s *usageDeleteRepoStub) GetByID(context.Context, int64) (*UsageLog, error) {
	return s.usageLog, nil
}

func (s *usageDeleteRepoStub) Delete(_ context.Context, id int64) error {
	s.deleted = id
	return nil
}

type usageCostRecomputerStub struct {
	calls int
	start time.Time
	end   time.Time
	err   error
}

func (s *usageCostRecomputerStub) TriggerRecomputeRange(start, end time.Time) error {
	s.calls++
	s.start = start
	s.end = end
	return s.err
}

func TestUsageServiceDeleteReportsCostRecomputeSchedulingFailure(t *testing.T) {
	repo := &usageDeleteRepoStub{usageLog: &UsageLog{ID: 42, CreatedAt: time.Now().UTC()}}
	recomputer := &usageCostRecomputerStub{err: errors.New("scheduler unavailable")}
	svc := &UsageService{usageRepo: repo, costRecomputer: recomputer}

	err := svc.Delete(context.Background(), 42)

	require.ErrorContains(t, err, "delete usage log completed but schedule cost recompute")
	require.Equal(t, int64(42), repo.deleted)
	require.Equal(t, 1, recomputer.calls)
}

func TestUsageServiceDeleteRecomputesAffectedHour(t *testing.T) {
	createdAt := time.Date(2026, 8, 11, 13, 27, 45, 0, time.FixedZone("UTC+8", 8*60*60))
	repo := &usageDeleteRepoStub{usageLog: &UsageLog{ID: 42, CreatedAt: createdAt}}
	recomputer := &usageCostRecomputerStub{}
	svc := &UsageService{usageRepo: repo, costRecomputer: recomputer}

	require.NoError(t, svc.Delete(context.Background(), 42))
	require.Equal(t, int64(42), repo.deleted)
	require.Equal(t, 1, recomputer.calls)
	expectedStart := createdAt.UTC().Truncate(time.Hour)
	require.Equal(t, expectedStart, recomputer.start)
	require.Equal(t, expectedStart.Add(time.Hour), recomputer.end)
}
