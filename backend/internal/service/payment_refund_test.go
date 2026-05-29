//go:build unit

package service

import (
	"context"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestValidateRefundRequestRejectsLegacyGuessedProviderInstance(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("refund-legacy@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-legacy-user").
		Save(ctx)
	require.NoError(t, err)

	_, err = client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-refund-instance").
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetAllowUserRefund(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("REFUND-LEGACY-ORDER").
		SetOutTradeNo("sub2_refund_legacy_order").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-legacy-refund").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient: client,
	}

	_, err = svc.validateRefundRequest(ctx, order.ID, user.ID)
	require.Error(t, err)
	require.Equal(t, "USER_REFUND_DISABLED", infraerrors.Reason(err))
}

func TestPrepareRefundRejectsLegacyGuessedProviderInstance(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("refund-legacy-admin@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-legacy-admin-user").
		Save(ctx)
	require.NoError(t, err)

	_, err = client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-refund-admin-instance").
		SetConfig("{}").
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetAllowUserRefund(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(188).
		SetPayAmount(188).
		SetFeeRate(0).
		SetRechargeCode("REFUND-LEGACY-ADMIN-ORDER").
		SetOutTradeNo("sub2_refund_legacy_admin_order").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-legacy-admin-refund").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient: client,
	}

	plan, result, err := svc.PrepareRefund(ctx, order.ID, 0, "", false, false, 0)
	require.Nil(t, plan)
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, "REFUND_DISABLED", infraerrors.Reason(err))
}

func TestGwRefundRejectsAlipayMerchantIdentitySnapshotMismatch(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("refund-snapshot-mismatch@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-snapshot-mismatch-user").
		Save(ctx)
	require.NoError(t, err)

	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeAlipay).
		SetName("alipay-refund-mismatch-instance").
		SetConfig(encryptWebhookProviderConfig(t, map[string]string{
			"appId":      "runtime-alipay-app",
			"privateKey": "runtime-private-key",
		})).
		SetSupportedTypes("alipay").
		SetEnabled(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	instID := strconv.FormatInt(inst.ID, 10)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("REFUND-SNAPSHOT-MISMATCH-ORDER").
		SetOutTradeNo("sub2_refund_snapshot_mismatch_order").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-refund-snapshot-mismatch").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(instID).
		SetProviderKey(payment.TypeAlipay).
		SetProviderSnapshot(map[string]any{
			"schema_version":       2,
			"provider_instance_id": instID,
			"provider_key":         payment.TypeAlipay,
			"merchant_app_id":      "expected-alipay-app",
		}).
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient:    client,
		loadBalancer: newWebhookProviderTestLoadBalancer(client),
	}

	err = svc.gwRefund(ctx, &RefundPlan{
		OrderID:       order.ID,
		Order:         order,
		RefundAmount:  order.Amount,
		GatewayAmount: order.Amount,
		Reason:        "snapshot mismatch",
	})
	require.ErrorContains(t, err, "alipay app_id mismatch")
}

func TestCalculateGatewayRefundAmountUsesCurrencyPrecision(t *testing.T) {
	require.InDelta(t, 6.173, calculateGatewayRefundAmount(100, 12.345, 50, "KWD"), 1e-12)
	require.InDelta(t, 12.345, calculateGatewayRefundAmount(100, 12.345, 100, "KWD"), 1e-12)
	require.InDelta(t, 52, calculateGatewayRefundAmount(100, 103, 50, "JPY"), 1e-12)
}

func TestFormatGatewayRefundAmountUsesOrderCurrency(t *testing.T) {
	order := &dbent.PaymentOrder{
		ProviderSnapshot: map[string]any{
			"currency": "KWD",
		},
	}

	require.Equal(t, "12.345", formatGatewayRefundAmount(12.345, order))
}

func TestValidateRefundProviderResponseAcceptsPending(t *testing.T) {
	require.NoError(t, validateRefundProviderResponse(&payment.RefundResponse{Status: payment.ProviderStatusPending}))
	require.NoError(t, validateRefundProviderResponse(&payment.RefundResponse{Status: payment.ProviderStatusSuccess}))
	require.Error(t, validateRefundProviderResponse(&payment.RefundResponse{Status: payment.ProviderStatusFailed}))
	require.Error(t, validateRefundProviderResponse(nil))
}

func TestSubscriptionRefundCalculationsUseDaysAndCurrencyFloor(t *testing.T) {
	require.Equal(t, 33.33, calculateSubscriptionRefundAmountByDays(30, 100, 10))
	require.Equal(t, 100.0, calculateSubscriptionRefundAmountByDays(30, 100, 31))
	require.Equal(t, 1, calculateSubscriptionRefundDays(30, 100, 0.01))
	require.Equal(t, 4, calculateSubscriptionRefundDays(30, 100, 10.01))
	require.Equal(t, 30, calculateSubscriptionRefundDays(30, 100, 100.01))
}

func TestBuildRefundPreviewCapsPartiallyRefundedSubscription(t *testing.T) {
	days := 30
	completedAt := time.Now().Add(-20 * 24 * time.Hour)
	groupID := int64(11)
	order := &dbent.PaymentOrder{
		UserID:              7,
		Amount:              100,
		Status:              OrderStatusPartiallyRefunded,
		RefundAmount:        80,
		OrderType:           payment.OrderTypeSubscription,
		SubscriptionGroupID: &groupID,
		SubscriptionDays:    &days,
		CompletedAt:         &completedAt,
	}

	preview := (&PaymentService{}).BuildRefundPreview(context.Background(), order)

	require.Equal(t, 10, preview.SubscriptionRemainingDays)
	require.Equal(t, 20.0, preview.SuggestedRefundAmount)
	require.Equal(t, 10, preview.SuggestedSubscriptionDaysToDeduct)
}

func TestBuildRefundPreviewUsesSubscriptionCreatedByOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("refund-order-sub@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-order-sub-user").
		Save(ctx)
	require.NoError(t, err)

	group, err := client.Group.Create().
		SetName("refund-order-sub-group").
		SetStatus(StatusActive).
		SetSubscriptionType(SubscriptionTypeSubscription).
		Save(ctx)
	require.NoError(t, err)

	days := 30
	completedAt := time.Now().UTC()
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode("REFUND-ORDER-SUB").
		SetOutTradeNo("sub2_refund_order_sub").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-refund-order-sub").
		SetOrderType(payment.OrderTypeSubscription).
		SetStatus(OrderStatusCompleted).
		SetSubscriptionGroupID(group.ID).
		SetSubscriptionDays(days).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(completedAt).
		SetCompletedAt(completedAt).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	now := time.Now().UTC()
	_, err = client.UserSubscription.Create().
		SetUserID(user.ID).
		SetGroupID(group.ID).
		SetStartsAt(now.Add(-24 * time.Hour)).
		SetExpiresAt(now.Add(5*24*time.Hour - time.Minute)).
		SetStatus(SubscriptionStatusActive).
		SetAssignedAt(now).
		SetNotes("payment order " + strconv.FormatInt(order.ID, 10)).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.UserSubscription.Create().
		SetUserID(user.ID).
		SetGroupID(group.ID).
		SetStartsAt(now).
		SetExpiresAt(now.Add(20 * 24 * time.Hour)).
		SetStatus(SubscriptionStatusActive).
		SetAssignedAt(now).
		SetNotes("payment order 999999").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		subscriptionSvc: &SubscriptionService{entClient: client},
	}
	preview := svc.BuildRefundPreview(ctx, order)

	require.Equal(t, 5, preview.SubscriptionRemainingDays)
	require.Equal(t, 5, preview.SuggestedSubscriptionDaysToDeduct)
	require.Equal(t, 16.66, preview.SuggestedRefundAmount)
}
