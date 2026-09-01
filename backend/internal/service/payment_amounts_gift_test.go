package service

import "testing"

func TestCalculateRechargeGiftUsesBestMatchingTierAndRounds(t *testing.T) {
	tiers := []RechargeGiftTier{{Threshold: 30, Percent: 2}, {Threshold: 100, Percent: 12}, {Threshold: 200, Percent: 8}}
	if got := calculateRechargeGift(125.555, true, tiers); got != 15.07 {
		t.Fatalf("gift = %v, want 15.07", got)
	}
}

func TestCalculateRechargeGiftDisabledOrBelowThreshold(t *testing.T) {
	tiers := []RechargeGiftTier{{Threshold: 30, Percent: 2}}
	if got := calculateRechargeGift(29.99, true, tiers); got != 0 {
		t.Fatalf("below threshold gift = %v, want 0", got)
	}
	if got := calculateRechargeGift(100, false, tiers); got != 0 {
		t.Fatalf("disabled gift = %v, want 0", got)
	}
}
