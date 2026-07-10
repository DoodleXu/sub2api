package service

import (
	"context"
	"math"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

const minimumSubscriptionUpgradePayable = 0.01

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
		return subscriptionUpgradeCredit{}, infraerrors.BadRequest("UPGRADE_GROUP_MISMATCH", "selected subscription cannot be credited toward this plan")
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
	paidAmount, paidDays := subscriptionUpgradePaidTotals(orders)
	if paidDays <= 0 || paidAmount <= 0 {
		return subscriptionUpgradeCredit{}, nil
	}

	remainingDays := daysRemainingFromNow(sub.ExpiresAt)
	if remainingDays <= 0 {
		return subscriptionUpgradeCredit{}, nil
	}
	unit := decimal.NewFromFloat(paidAmount).Div(decimal.NewFromInt(int64(paidDays)))
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

func validateSubscriptionUpgradeSourceForFulfillment(sub *UserSubscription, order *dbent.PaymentOrder) error {
	if err := validateSubscriptionUpgradeSourceIdentity(sub, order); err != nil {
		return err
	}
	if sub.Status != SubscriptionStatusActive || !sub.ExpiresAt.After(time.Now()) {
		return infraerrors.BadRequest("UPGRADE_SUBSCRIPTION_NOT_ACTIVE", "selected subscription is no longer active")
	}
	return nil
}

func validateSubscriptionUpgradeSourceIdentity(sub *UserSubscription, order *dbent.PaymentOrder) error {
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
		return infraerrors.BadRequest("UPGRADE_GROUP_MISMATCH", "selected subscription cannot be credited toward this plan")
	}
	return nil
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
