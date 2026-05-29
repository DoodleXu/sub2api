package service

import "testing"

func floatPtr(v float64) *float64 { return &v }

func TestSubscriptionTypePolicy(t *testing.T) {
	daily := floatPtr(10)
	weekly := floatPtr(70)
	monthly := floatPtr(300)

	tests := []struct {
		name             string
		subscriptionType string
		wantSubscription bool
		wantDaily        bool
		wantWeekly       bool
		wantMonthly      bool
	}{
		{"standard", SubscriptionTypeStandard, false, false, false, false},
		{"monthly", SubscriptionTypeSubscription, true, true, true, true},
		{"weekly", SubscriptionTypeSubscriptionWeekly, true, true, true, false},
		{"daily", SubscriptionTypeSubscriptionDaily, true, true, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSubscriptionTypeValue(tt.subscriptionType); got != tt.wantSubscription {
				t.Fatalf("IsSubscriptionTypeValue() = %v, want %v", got, tt.wantSubscription)
			}
			g := &Group{
				SubscriptionType: tt.subscriptionType,
				DailyLimitUSD:    daily,
				WeeklyLimitUSD:   weekly,
				MonthlyLimitUSD:  monthly,
			}
			if got := g.HasDailyLimit(); got != tt.wantDaily {
				t.Fatalf("HasDailyLimit() = %v, want %v", got, tt.wantDaily)
			}
			if got := g.HasWeeklyLimit(); got != tt.wantWeekly {
				t.Fatalf("HasWeeklyLimit() = %v, want %v", got, tt.wantWeekly)
			}
			if got := g.HasMonthlyLimit(); got != tt.wantMonthly {
				t.Fatalf("HasMonthlyLimit() = %v, want %v", got, tt.wantMonthly)
			}
		})
	}
}

func TestApplySubscriptionLimitPolicy(t *testing.T) {
	daily := floatPtr(10)
	weekly := floatPtr(70)
	monthly := floatPtr(300)

	_, gotWeekly, gotMonthly := applySubscriptionLimitPolicy(SubscriptionTypeSubscriptionDaily, daily, weekly, monthly)
	if gotWeekly != nil || gotMonthly != nil {
		t.Fatalf("daily subscription should clear weekly and monthly limits, got weekly=%v monthly=%v", gotWeekly, gotMonthly)
	}

	_, gotWeekly, gotMonthly = applySubscriptionLimitPolicy(SubscriptionTypeSubscriptionWeekly, daily, weekly, monthly)
	if gotWeekly == nil || gotMonthly != nil {
		t.Fatalf("weekly subscription should keep weekly and clear monthly, got weekly=%v monthly=%v", gotWeekly, gotMonthly)
	}
}
