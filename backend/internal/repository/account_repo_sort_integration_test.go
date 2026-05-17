//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *AccountRepoSuite) TestList_DefaultSortByNameAsc() {
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "z-account"})
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "a-account"})

	accounts, _, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10})
	s.Require().NoError(err)
	s.Require().Len(accounts, 2)
	s.Require().Equal("a-account", accounts[0].Name)
	s.Require().Equal("z-account", accounts[1].Name)
}

func (s *AccountRepoSuite) TestListWithFilters_SortByPriorityDesc() {
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "low-priority", Priority: 10})
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "high-priority", Priority: 90})

	accounts, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "priority",
		SortOrder: "desc",
	}, "", "", "", "", 0, "")
	s.Require().NoError(err)
	s.Require().Len(accounts, 2)
	s.Require().Equal("high-priority", accounts[0].Name)
	s.Require().Equal("low-priority", accounts[1].Name)
}

func (s *AccountRepoSuite) TestListWithFilters_SortByStatusAscUsesDisplayStatusPriority() {
	now := time.Now()
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name:      "05-disabled",
		Status:    service.StatusDisabled,
		CreatedAt: now.Add(-6 * time.Minute),
	})
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name:      "04-error",
		Status:    service.StatusError,
		CreatedAt: now.Add(-5 * time.Minute),
	})
	paused := mustCreateAccount(s.T(), s.client, &service.Account{
		Name:      "02-paused",
		CreatedAt: now.Add(-45 * time.Second),
	})
	s.Require().NoError(s.client.Account.UpdateOneID(paused.ID).SetSchedulable(false).Exec(s.ctx))
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name:                   "04-temp-unsched",
		TempUnschedulableUntil: ptrAccountSortTime(now.Add(10 * time.Minute)),
		CreatedAt:              now.Add(-4 * time.Minute),
	})
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name:             "03-rate-limit-5m",
		RateLimitResetAt: ptrAccountSortTime(now.Add(5 * time.Minute)),
		CreatedAt:        now.Add(-3 * time.Minute),
	})
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name:             "03-rate-limit-3m",
		RateLimitResetAt: ptrAccountSortTime(now.Add(3 * time.Minute)),
		CreatedAt:        now.Add(-2 * time.Minute),
	})
	mustCreateAccount(s.T(), s.client, &service.Account{
		Name:      "01-normal",
		CreatedAt: now.Add(-1 * time.Minute),
	})

	accounts, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "status",
		SortOrder: "asc",
	}, "", "", "", "", 0, "")
	s.Require().NoError(err)
	s.Require().Len(accounts, 7)
	s.Require().Equal([]string{
		"01-normal",
		"02-paused",
		"03-rate-limit-3m",
		"03-rate-limit-5m",
		"04-temp-unsched",
		"04-error",
		"05-disabled",
	}, accountNames(accounts))
}

func ptrAccountSortTime(t time.Time) *time.Time {
	return &t
}

func accountNames(accounts []service.Account) []string {
	names := make([]string, 0, len(accounts))
	for _, account := range accounts {
		names = append(names, account.Name)
	}
	return names
}
