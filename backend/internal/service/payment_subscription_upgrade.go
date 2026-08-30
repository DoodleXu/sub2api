package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

const minimumSubscriptionUpgradePayable = 0.01

const subscriptionUpgradeRiskUsageThreshold = 0.90

type subscriptionUpgradeCredit struct {
	SubscriptionID int64
	CreditAmount   float64
	CreditDays     int
}

func (s *PaymentService) ListSubscriptionUpgradeOptions(ctx context.Context, userID, planID int64) ([]SubscriptionUpgradeOption, error) {
	plan, err := s.validateSubOrder(ctx, CreateOrderRequest{PlanID: planID, OrderType: payment.OrderTypeSubscription})
	if err != nil {
		return nil, err
	}
	subs, err := s.subscriptionSvc.ListActiveUserSubscriptions(ctx, userID)
	if err != nil {
		return nil, err
	}

	options := make([]SubscriptionUpgradeOption, 0, len(subs))
	for _, sub := range subs {
		credit, err := s.calculateSubscriptionUpgradeCredit(ctx, userID, &sub, plan)
		if err != nil || credit.CreditAmount <= 0 {
			continue
		}
		groupName := ""
		groupPlatform := ""
		if sub.Group != nil {
			groupName = sub.Group.Name
			groupPlatform = sub.Group.Platform
		}
		options = append(options, SubscriptionUpgradeOption{
			SubscriptionID: sub.ID,
			GroupID:        sub.GroupID,
			GroupName:      groupName,
			GroupPlatform:  groupPlatform,
			ExpiresAt:      sub.ExpiresAt.Format(time.RFC3339),
			DaysRemaining:  daysRemainingFromNow(sub.ExpiresAt),
			CreditAmount:   credit.CreditAmount,
			CreditDays:     credit.CreditDays,
			PayableAmount:  subscriptionUpgradePayableAmount(plan.Price, credit.CreditAmount),
		})
	}
	return options, nil
}

func (s *PaymentService) prepareSubscriptionUpgradeCredit(ctx context.Context, userID int64, plan *dbent.SubscriptionPlan, subscriptionID int64) (*subscriptionUpgradeCredit, error) {
	if subscriptionID <= 0 {
		return nil, nil
	}
	sub, err := s.subscriptionSvc.GetByID(ctx, subscriptionID)
	if err != nil {
		return nil, infraerrors.BadRequest("UPGRADE_SUBSCRIPTION_NOT_FOUND", "selected subscription is not available")
	}
	credit, err := s.calculateSubscriptionUpgradeCredit(ctx, userID, sub, plan)
	if err != nil {
		return nil, err
	}
	if credit.CreditAmount <= 0 {
		return nil, infraerrors.BadRequest("UPGRADE_CREDIT_UNAVAILABLE", "selected subscription has no remaining paid value")
	}
	return &credit, nil
}

func (s *PaymentService) calculateSubscriptionUpgradeCredit(ctx context.Context, userID int64, sub *UserSubscription, targetPlan *dbent.SubscriptionPlan) (subscriptionUpgradeCredit, error) {
	if sub == nil || targetPlan == nil || sub.UserID != userID || sub.Status != SubscriptionStatusActive || !sub.ExpiresAt.After(time.Now()) {
		return subscriptionUpgradeCredit{}, infraerrors.BadRequest("UPGRADE_SUBSCRIPTION_NOT_ACTIVE", "selected subscription is not active")
	}
	if sub.GroupID != targetPlan.GroupID {
		matches, err := s.subscriptionUpgradeGroupsSharePlatform(ctx, sub.GroupID, targetPlan.GroupID)
		if err != nil {
			return subscriptionUpgradeCredit{}, err
		}
		if !matches {
			return subscriptionUpgradeCredit{}, infraerrors.BadRequest("UPGRADE_GROUP_MISMATCH", "selected subscription cannot be credited toward this plan")
		}
	}

	orders, err := s.entClient.PaymentOrder.Query().
		Where(
			paymentorder.UserIDEQ(userID),
			paymentorder.OrderTypeEQ(payment.OrderTypeSubscription),
			paymentorder.FulfilledSubscriptionIDEQ(sub.ID),
			paymentorder.StatusIn(OrderStatusCompleted, OrderStatusPartiallyRefunded),
			paymentorder.SubscriptionDaysNotNil(),
		).
		Order(dbent.Asc(paymentorder.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return subscriptionUpgradeCredit{}, err
	}
	if len(orders) == 0 {
		orders, err = s.entClient.PaymentOrder.Query().
			Where(
				paymentorder.UserIDEQ(userID),
				paymentorder.OrderTypeEQ(payment.OrderTypeSubscription),
				paymentorder.SubscriptionGroupIDEQ(sub.GroupID),
				paymentorder.StatusIn(OrderStatusCompleted, OrderStatusPartiallyRefunded),
				paymentorder.SubscriptionDaysNotNil(),
				paymentorder.CreatedAtGTE(subscriptionUpgradeOrderWindowStart(sub)),
			).
			Order(dbent.Asc(paymentorder.FieldCreatedAt)).
			All(ctx)
	}
	if err != nil {
		return subscriptionUpgradeCredit{}, err
	}
	for _, order := range orders {
		if order != nil && order.PlanID != nil && *order.PlanID == targetPlan.ID {
			return subscriptionUpgradeCredit{}, infraerrors.BadRequest("UPGRADE_SAME_PLAN_NOT_ALLOWED", "the same subscription plan must be renewed instead of upgraded")
		}
	}
	paidAmount, paidDays := subscriptionUpgradePaidTotals(orders)
	if paidDays <= 0 || paidAmount <= 0 {
		return subscriptionUpgradeCredit{}, nil
	}

	remainingDays := daysRemainingFromNow(sub.ExpiresAt)
	if remainingDays <= 0 {
		return subscriptionUpgradeCredit{}, nil
	}
	unit := decimal.NewFromFloat(paidAmount).Div(decimal.NewFromInt(int64(paidDays)))
	if err := validateSubscriptionUpgradeTargetPrice(targetPlan.Price, unit.InexactFloat64()); err != nil {
		return subscriptionUpgradeCredit{}, err
	}
	rawCredit := unit.Mul(decimal.NewFromInt(int64(remainingDays))).Round(2).InexactFloat64()
	if rawCredit <= 0 {
		return subscriptionUpgradeCredit{}, nil
	}
	return subscriptionUpgradeCredit{
		SubscriptionID: sub.ID,
		CreditAmount:   subscriptionUpgradeCreditAmount(targetPlan.Price, rawCredit),
		CreditDays:     remainingDays,
	}, nil
}

func validateSubscriptionUpgradeTargetPrice(targetPrice, sourceUnitPrice float64) error {
	if targetPrice <= sourceUnitPrice {
		return infraerrors.BadRequest("UPGRADE_TARGET_NOT_HIGHER", "target subscription plan must be strictly more expensive than the source plan")
	}
	return nil
}

func (s *PaymentService) validateSubscriptionUpgradeSourceForFulfillment(ctx context.Context, sub *UserSubscription, order *dbent.PaymentOrder) error {
	if err := s.validateSubscriptionUpgradeSourceIdentity(ctx, sub, order); err != nil {
		return err
	}
	if sub.Status != SubscriptionStatusActive || !sub.ExpiresAt.After(time.Now()) {
		return infraerrors.BadRequest("UPGRADE_SUBSCRIPTION_NOT_ACTIVE", "selected subscription is no longer active")
	}
	group := sub.Group
	if group == nil && s.groupRepo != nil {
		group, _ = s.groupRepo.GetByID(ctx, sub.GroupID)
	}
	if group != nil {
		if (group.DailyLimitUSD != nil && sub.DailyUsageUSD >= *group.DailyLimitUSD) ||
			(group.WeeklyLimitUSD != nil && sub.WeeklyUsageUSD >= *group.WeeklyLimitUSD) ||
			(group.MonthlyLimitUSD != nil && sub.MonthlyUsageUSD >= *group.MonthlyLimitUSD) {
			return infraerrors.Conflict("UPGRADE_SOURCE_QUOTA_EXHAUSTED", "source subscription quota was exhausted before fulfillment; please create a new order")
		}
	}
	if order.PlanID != nil && s.configService != nil {
		plan, err := s.configService.GetPlan(ctx, *order.PlanID)
		if err != nil {
			return infraerrors.BadRequest("UPGRADE_PLAN_NOT_AVAILABLE", "upgrade target plan is no longer available")
		}
		current, err := s.calculateSubscriptionUpgradeCredit(ctx, order.UserID, sub, plan)
		if err != nil {
			return err
		}
		if order.UpgradeCreditDays == nil || current.SubscriptionID != sub.ID || current.CreditDays != *order.UpgradeCreditDays || math.Abs(current.CreditAmount-order.UpgradeCreditAmount) > 0.005 {
			return infraerrors.Conflict("UPGRADE_CREDIT_STALE", "subscription upgrade credit changed after the order was created; please create a new order")
		}
	}
	return nil
}

// emitSubscriptionUpgradeRiskAlert records a structured warning in the Ops
// system-log sink. It is deliberately best-effort: an alert failure must never
// make an otherwise valid payment order fail.
func (s *PaymentService) emitSubscriptionUpgradeRiskAlert(ctx context.Context, order *dbent.PaymentOrder, sub *UserSubscription, plan *dbent.SubscriptionPlan, credit *subscriptionUpgradeCredit) {
	if order == nil || sub == nil || plan == nil || credit == nil {
		return
	}
	group := sub.Group
	if group == nil && s.groupRepo != nil {
		group, _ = s.groupRepo.GetByID(ctx, sub.GroupID)
	}
	reasons := make([]string, 0, 4)
	usageRatio := func(used float64, limit *float64, name string) float64 {
		if limit == nil || *limit <= 0 {
			return 0
		}
		ratio := used / *limit
		if ratio >= subscriptionUpgradeRiskUsageThreshold {
			reasons = append(reasons, fmt.Sprintf("%s_usage_%.0f%%", name, ratio*100))
		}
		return ratio
	}
	dailyRatio, weeklyRatio, monthlyRatio := 0.0, 0.0, 0.0
	if group != nil {
		dailyRatio = usageRatio(sub.DailyUsageUSD, group.DailyLimitUSD, "daily")
		weeklyRatio = usageRatio(sub.WeeklyUsageUSD, group.WeeklyLimitUSD, "weekly")
		monthlyRatio = usageRatio(sub.MonthlyUsageUSD, group.MonthlyLimitUSD, "monthly")
	}
	creditRatio := 0.0
	if plan.Price > 0 {
		creditRatio = credit.CreditAmount / plan.Price
		if creditRatio >= subscriptionUpgradeRiskUsageThreshold {
			reasons = append(reasons, fmt.Sprintf("credit_%.0f%%", creditRatio*100))
		}
	}
	if subscriptionUpgradePayableAmount(plan.Price, credit.CreditAmount) <= minimumSubscriptionUpgradePayable {
		reasons = append(reasons, "minimum_payable")
	}
	if len(reasons) == 0 {
		return
	}
	slog.WarnContext(ctx, "high-risk subscription upgrade detected",
		"component", "payment.upgrade_risk",
		"risk_code", "SUBSCRIPTION_UPGRADE_HIGH_RISK",
		"order_id", order.ID,
		"user_id", order.UserID,
		"subscription_id", sub.ID,
		"plan_id", plan.ID,
		"group_id", sub.GroupID,
		"reasons", reasons,
		"daily_usage_ratio", dailyRatio,
		"weekly_usage_ratio", weeklyRatio,
		"monthly_usage_ratio", monthlyRatio,
		"credit_ratio", creditRatio,
		"credit_amount", credit.CreditAmount,
		"credit_days", credit.CreditDays,
		"payable_amount", subscriptionUpgradePayableAmount(plan.Price, credit.CreditAmount),
	)
}

func (s *PaymentService) validateSubscriptionUpgradeSourceIdentity(ctx context.Context, sub *UserSubscription, order *dbent.PaymentOrder) error {
	if sub == nil {
		return infraerrors.BadRequest("UPGRADE_SUBSCRIPTION_NOT_FOUND", "selected subscription is not available")
	}
	if order == nil || order.SubscriptionGroupID == nil {
		return infraerrors.BadRequest("UPGRADE_ORDER_INVALID", "subscription order is missing upgrade target")
	}
	if sub.UserID != order.UserID {
		return infraerrors.BadRequest("UPGRADE_SUBSCRIPTION_NOT_OWNER", "selected subscription does not belong to the order user")
	}
	if sub.GroupID != *order.SubscriptionGroupID {
		matches, err := s.subscriptionUpgradeGroupsSharePlatform(ctx, sub.GroupID, *order.SubscriptionGroupID)
		if err != nil {
			return err
		}
		if !matches {
			return infraerrors.BadRequest("UPGRADE_GROUP_MISMATCH", "selected subscription cannot be credited toward this plan")
		}
	}
	return nil
}

func (s *PaymentService) subscriptionUpgradeGroupPlatform(ctx context.Context, groupID int64) (string, error) {
	if s.groupRepo != nil {
		g, err := s.groupRepo.GetByID(ctx, groupID)
		if err != nil {
			return "", err
		}
		if g == nil {
			return "", ErrGroupNotFound
		}
		return g.Platform, nil
	}
	if s.entClient == nil {
		return "", ErrGroupNotFound
	}
	g, err := s.entClient.Group.Get(ctx, groupID)
	if err != nil {
		return "", err
	}
	return g.Platform, nil
}

func (s *PaymentService) subscriptionUpgradeGroupsSharePlatform(ctx context.Context, sourceGroupID, targetGroupID int64) (bool, error) {
	sourcePlatform, err := s.subscriptionUpgradeGroupPlatform(ctx, sourceGroupID)
	if err != nil {
		return false, err
	}
	targetPlatform, err := s.subscriptionUpgradeGroupPlatform(ctx, targetGroupID)
	if err != nil {
		return false, err
	}
	return sourcePlatform != "" && sourcePlatform == targetPlatform, nil
}

func subscriptionUpgradeCreditAmount(targetPrice, rawCredit float64) float64 {
	if targetPrice <= minimumSubscriptionUpgradePayable {
		return 0
	}
	maxCredit := targetPrice - minimumSubscriptionUpgradePayable
	if rawCredit > maxCredit {
		rawCredit = maxCredit
	}
	return decimal.NewFromFloat(rawCredit).Round(2).InexactFloat64()
}

func subscriptionUpgradePaidAmount(order *dbent.PaymentOrder) float64 {
	if order == nil || order.Amount <= 0 {
		return 0
	}
	if order.Status != OrderStatusPartiallyRefunded || order.RefundAmount <= 0 {
		return order.Amount
	}
	paid := decimal.NewFromFloat(order.Amount).Sub(decimal.NewFromFloat(order.RefundAmount)).Round(2).InexactFloat64()
	if paid <= 0 {
		return 0
	}
	return paid
}

func subscriptionUpgradePaidTotals(orders []*dbent.PaymentOrder) (float64, int) {
	totalPaid := decimal.Zero
	totalDays := 0
	for _, order := range orders {
		if order == nil || order.SubscriptionDays == nil || *order.SubscriptionDays <= 0 {
			continue
		}
		paid := subscriptionUpgradePaidAmount(order)
		if paid <= 0 {
			continue
		}
		totalPaid = totalPaid.Add(decimal.NewFromFloat(paid))
		totalDays += *order.SubscriptionDays
	}
	return totalPaid.Round(2).InexactFloat64(), totalDays
}

func subscriptionUpgradeOrderWindowStart(sub *UserSubscription) time.Time {
	if sub == nil || sub.StartsAt.IsZero() {
		return time.Time{}
	}
	return sub.StartsAt.Add(-time.Hour)
}

func subscriptionUpgradePayableAmount(targetPrice, credit float64) float64 {
	payable := decimal.NewFromFloat(targetPrice).Sub(decimal.NewFromFloat(credit)).Round(2).InexactFloat64()
	if payable < minimumSubscriptionUpgradePayable {
		return minimumSubscriptionUpgradePayable
	}
	return payable
}

func daysRemainingFromNow(expiresAt time.Time) int {
	hours := time.Until(expiresAt).Hours()
	if hours <= 0 {
		return 0
	}
	return int(math.Ceil(hours / 24))
}
