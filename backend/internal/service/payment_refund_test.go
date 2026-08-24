//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
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
		entClient:    client,
		loadBalancer: &captureLoadBalancer{},
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
		entClient:    client,
		loadBalancer: &captureLoadBalancer{},
	}

	plan, result, err := svc.PrepareRefund(ctx, order.ID, 0, "", false, false, 0)
	require.Nil(t, plan)
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, "REFUND_DISABLED", infraerrors.Reason(err))
}

func TestPrepareRefundRetryIgnoresFailedRequestAmount(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "failed-request-watermark")
	_, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetStatus(OrderStatusRefundFailed).
		SetRefundAmount(order.Amount).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(order.ID, 10)).
		SetAction("REFUND_ATTEMPT").
		SetOperator("admin").
		SetDetail(`{"refundID":"rf_failed_attempt","attemptID":"rf_failed_attempt","refundAmount":100,"previousRefundAmount":0,"deductionRollbackOK":true,"deductionApplied":false}`).
		Save(ctx)
	require.NoError(t, err)

	plan, result, err := (&PaymentService{entClient: client}).PrepareRefund(ctx, order.ID, 0, "retry", true, false, 0)
	require.NoError(t, err)
	require.Nil(t, result)
	require.NotNil(t, plan)
	require.Zero(t, plan.PreviousRefundAmount)
	require.Equal(t, order.Amount, plan.RefundAmount)
}

func TestPrepDeductMalformedSubscriptionDoesNotPersistPhantomDays(t *testing.T) {
	order := &dbent.PaymentOrder{OrderType: payment.OrderTypeSubscription, Amount: 100}
	plan := &RefundPlan{RefundAmount: 50, SubDaysToDeduct: 7}
	svc := &PaymentService{}

	warning := svc.prepDeduct(context.Background(), order, plan, false)
	require.NotNil(t, warning)
	require.True(t, warning.RequireForce)
	require.Zero(t, plan.SubDaysToDeduct)

	plan.SubDaysToDeduct = 7
	require.Nil(t, svc.prepDeduct(context.Background(), order, plan, true))
	require.Zero(t, plan.SubDaysToDeduct)
}

func TestRefundPendingOrderCannotStartAnotherGatewayRefund(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("refund-pending-reentry@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-pending-reentry").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode("REFUND-PENDING-REENTRY").
		SetOutTradeNo("refund_pending_reentry").
		SetPaymentType(payment.TypeStripe).
		SetPaymentTradeNo("pi_refund_pending_reentry").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusRefundPending).
		SetRefundAmount(20).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	plan, result, err := svc.PrepareRefund(ctx, order.ID, 0, "retry pending refund", false, false, 0)
	require.Nil(t, plan)
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, "INVALID_STATUS", infraerrors.Reason(err))

	stalePlan := &RefundPlan{
		OrderID:       order.ID,
		Order:         order,
		RefundAmount:  20,
		GatewayAmount: 20,
		Reason:        "stale retry",
	}
	result, err = svc.ExecuteRefund(ctx, stalePlan)
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
	require.Equal(t, 20.0, reloaded.RefundAmount)
}

func TestPrepDeductBalanceRequiresForceWhenBalanceIsInsufficient(t *testing.T) {
	for _, tc := range []struct {
		name        string
		balance     float64
		force       bool
		wantDeduct  float64
		wantWarning bool
	}{
		{name: "insufficient balance", balance: 40, wantWarning: true},
		{name: "forced insufficient balance", balance: 40, force: true, wantDeduct: 40},
		{name: "equal balance", balance: 100, wantDeduct: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := &RefundPlan{RefundAmount: 100}
			svc := &PaymentService{userRepo: &mockUserRepo{getByIDUser: &User{Balance: tc.balance}}}

			result := svc.prepDeduct(context.Background(), &dbent.PaymentOrder{
				UserID:    1,
				OrderType: payment.OrderTypeBalance,
			}, plan, tc.force)

			if tc.wantWarning {
				require.NotNil(t, result)
				require.False(t, result.Success)
				require.True(t, result.RequireForce)
				require.Equal(t, "user balance is insufficient for deduction, use force", result.Warning)
				require.Zero(t, plan.BalanceToDeduct)
				return
			}
			require.Nil(t, result)
			require.Equal(t, payment.DeductionTypeBalance, plan.DeductionType)
			require.Equal(t, tc.wantDeduct, plan.BalanceToDeduct)
		})
	}
}

func TestExecuteRefundUsesActualAvailableBalanceDeduction(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("refund-execute-clamp@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-execute-clamp").
		Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode("REFUND-EXECUTE-CLAMP").
		SetOutTradeNo("refund_execute_clamp").
		SetPaymentType(payment.TypeStripe).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	repo := &mockUserRepo{deductAvailableBalanceFn: func(_ context.Context, id int64, amount float64) (float64, error) {
		require.Equal(t, user.ID, id)
		require.Equal(t, 100.0, amount)
		return 25, nil
	}}
	plan := &RefundPlan{
		OrderID: order.ID, Order: order, RefundAmount: 100, GatewayAmount: 100,
		Reason: "concurrent spend", Force: true, DeductionType: payment.DeductionTypeBalance, BalanceToDeduct: 100,
	}

	result, err := (&PaymentService{entClient: client, userRepo: repo}).ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 25.0, plan.BalanceToDeduct)
	require.Equal(t, 25.0, result.BalanceDeducted)
	audit, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
		Only(ctx)
	require.NoError(t, err)
	require.Contains(t, audit.Detail, `"balanceDeducted":25`)
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

	plan := &RefundPlan{
		OrderID:       order.ID,
		Order:         order,
		RefundAmount:  order.Amount,
		GatewayAmount: order.Amount,
		Reason:        "snapshot mismatch",
	}
	_, err = svc.gwRefund(ctx, plan)
	require.ErrorContains(t, err, "alipay app_id mismatch")
	require.False(t, isRefundGatewayOutcomeUnknown(err))

	result, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Success)
	require.Contains(t, result.Warning, "rolled back")
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	pendingAudits, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, pendingAudits)
}

func TestGwRefundDistinguishesExplicitFailureFromUnknownOutcome(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "gateway-outcome")
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}

	provider := &refundExecutionProviderTestDouble{
		refundResponse: &payment.RefundResponse{RefundID: "rf_failed", Status: payment.ProviderStatusFailed},
		refundErr:      errors.New("provider reported failure"),
	}
	restore := replacePaymentProviderFactoryForTest(t, provider)
	defer restore()

	resp, err := svc.gwRefund(ctx, &RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 10, GatewayAmount: 10})
	require.NoError(t, err)
	require.Equal(t, payment.ProviderStatusFailed, resp.Status)

	provider.refundResponse = nil
	provider.refundErr = context.DeadlineExceeded
	_, err = svc.gwRefund(ctx, &RefundPlan{OrderID: order.ID, Order: order, RefundAmount: 10, GatewayAmount: 10})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.True(t, isRefundGatewayOutcomeUnknown(err))
}

func TestCalculateGatewayRefundAmountUsesCurrencyPrecision(t *testing.T) {
	require.InDelta(t, 6.173, calculateGatewayRefundAmount(100, 12.345, 50, "KWD"), 1e-12)
	require.InDelta(t, 12.345, calculateGatewayRefundAmount(100, 12.345, 100, "KWD"), 1e-12)
	require.InDelta(t, 52, calculateGatewayRefundAmount(100, 103, 50, "JPY"), 1e-12)
}

func TestRefundProviderAttemptIDIsStablePerRefundTranche(t *testing.T) {
	order := &dbent.PaymentOrder{OutTradeNo: "merchant-order-1"}
	plan := &RefundPlan{OrderID: 1, Order: order, GatewayAmount: 12.34, PreviousRefundAmount: 0}
	first := refundProviderAttemptID(plan)
	require.NotEmpty(t, first)
	require.Equal(t, first, refundProviderAttemptID(plan))

	plan.PreviousRefundAmount = 12.34
	require.NotEqual(t, first, refundProviderAttemptID(plan), "later partial refunds need a distinct provider idempotency key")
}

func TestRefundProviderRetryIDIsUniqueForFailedAttempt(t *testing.T) {
	plan := &RefundPlan{OrderID: 1, Order: &dbent.PaymentOrder{OutTradeNo: "merchant-order-retry"}, GatewayAmount: 12.34}
	first := refundProviderRetryID(plan)
	second := refundProviderRetryID(plan)
	require.NotEmpty(t, first)
	require.NotEqual(t, first, second)
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

func TestFinishRefundPendingMarksOrderPendingAndRollsBackDeduction(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("refund-pending@example.com").
		SetPasswordHash("hash").
		SetUsername("refund-pending-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode("REFUND-PENDING-ORDER").
		SetOutTradeNo("sub2_refund_pending_order").
		SetPaymentType(payment.TypeStripe).
		SetPaymentTradeNo("pi_refund_pending").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusRefunding).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	var rolledBack float64
	userRepo := &mockUserRepo{}
	userRepo.adjustBalanceFn = func(ctx context.Context, id int64, amount float64) (BalanceChange, error) {
		require.Equal(t, user.ID, id)
		rolledBack += amount
		return BalanceChange{}, nil
	}
	svc := &PaymentService{
		entClient:    client,
		loadBalancer: &captureLoadBalancer{},
		userRepo:     userRepo,
	}
	plan := &RefundPlan{
		OrderID:         order.ID,
		Order:           order,
		RefundAmount:    40,
		GatewayAmount:   40,
		Reason:          "gateway accepted but not final",
		Force:           true,
		DeductionType:   payment.DeductionTypeBalance,
		BalanceToDeduct: 40,
	}

	result, err := svc.finishRefund(ctx, plan, &payment.RefundResponse{Status: payment.ProviderStatusPending})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Success)
	require.Contains(t, result.Warning, "pending confirmation")
	require.Equal(t, 40.0, rolledBack)
	require.Zero(t, plan.BalanceToDeduct)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
	require.Equal(t, 40.0, reloaded.RefundAmount)
	require.NotNil(t, reloaded.RefundReason)
	require.Equal(t, "gateway accepted but not final", *reloaded.RefundReason)
	require.Nil(t, reloaded.RefundAt)

	pendingAudits, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, pendingAudits)
	successAudits, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, successAudits)
}

func TestFinishRefundSuccessStatusesFinalize(t *testing.T) {
	for _, status := range []string{payment.ProviderStatusSuccess, payment.ProviderStatusRefunded} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)

			user, err := client.User.Create().
				SetEmail("refund-success-" + status + "@example.com").
				SetPasswordHash("hash").
				SetUsername("refund-success-" + status).
				Save(ctx)
			require.NoError(t, err)

			order, err := client.PaymentOrder.Create().
				SetUserID(user.ID).
				SetUserEmail(user.Email).
				SetUserName(user.Username).
				SetAmount(100).
				SetPayAmount(100).
				SetFeeRate(0).
				SetRechargeCode("REFUND-SUCCESS-" + status).
				SetOutTradeNo("sub2_refund_success_" + status).
				SetPaymentType(payment.TypeStripe).
				SetPaymentTradeNo("pi_refund_success_" + status).
				SetOrderType(payment.OrderTypeBalance).
				SetStatus(OrderStatusRefunding).
				SetExpiresAt(time.Now().Add(time.Hour)).
				SetPaidAt(time.Now()).
				SetClientIP("127.0.0.1").
				SetSrcHost("api.example.com").
				Save(ctx)
			require.NoError(t, err)

			svc := &PaymentService{entClient: client}
			plan := &RefundPlan{
				OrderID:         order.ID,
				Order:           order,
				RefundAmount:    100,
				GatewayAmount:   100,
				Reason:          "final success",
				DeductionType:   payment.DeductionTypeBalance,
				BalanceToDeduct: 100,
			}

			result, err := svc.finishRefund(ctx, plan, &payment.RefundResponse{Status: status})
			require.NoError(t, err)
			require.NotNil(t, result)
			require.True(t, result.Success)
			require.Equal(t, 100.0, result.BalanceDeducted)

			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusRefunded, reloaded.Status)
			require.NotNil(t, reloaded.RefundAt)

			successAudits, err := client.PaymentAuditLog.Query().
				Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
				Count(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, successAudits)
			pendingAudits, err := client.PaymentAuditLog.Query().
				Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
				Count(ctx)
			require.NoError(t, err)
			require.Zero(t, pendingAudits)
		})
	}
}

func TestQueryAndFinalizeRefundFinalizesProviderStatuses(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		wantStatus string
		wantDeduct float64
		available  float64
	}{
		{name: "success", status: payment.ProviderStatusSuccess, wantStatus: OrderStatusRefunded, wantDeduct: 100, available: 100},
		{name: "success clamps current balance", status: payment.ProviderStatusSuccess, wantStatus: OrderStatusRefunded, wantDeduct: 35, available: 35},
		{name: "failed", status: payment.ProviderStatusFailed, wantStatus: OrderStatusRefundFailed},
		{name: "pending", status: payment.ProviderStatusPending, wantStatus: OrderStatusRefundPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "query-finalize-"+tc.name)

			var deducted float64
			svc := &PaymentService{
				entClient:    client,
				loadBalancer: &captureLoadBalancer{},
				userRepo: &mockUserRepo{deductAvailableBalanceFn: func(ctx context.Context, id int64, amount float64) (float64, error) {
					deducted += tc.available
					return tc.available, nil
				}},
			}
			restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{
				refundResponse: &payment.RefundResponse{RefundID: "rf_test", Status: tc.status},
			})
			defer restore()

			result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, tc.status == payment.ProviderStatusSuccess, result.Success)
			require.Equal(t, tc.wantDeduct, deducted)
			if tc.status == payment.ProviderStatusSuccess {
				require.Equal(t, tc.wantDeduct, result.BalanceDeducted)
				audit, err := client.PaymentAuditLog.Query().
					Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
					Only(ctx)
				require.NoError(t, err)
				require.Contains(t, audit.Detail, `"refundID":"rf_test"`)
				require.Contains(t, audit.Detail, fmt.Sprintf(`"balanceDeducted":%v`, tc.wantDeduct))
			}

			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, reloaded.Status)
		})
	}
}

func TestQueryAndFinalizeRefundRecoversFailedRollbackBeforeTerminalFailure(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "query-finalize-rollback-recovery")
	pendingAudit, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
		Only(ctx)
	require.NoError(t, err)
	_, err = client.PaymentAuditLog.UpdateOneID(pendingAudit.ID).
		SetDetail(`{"refundID":"rf_test","refundAmount":100,"deductionRollbackOK":false,"deductionApplied":true,"balanceDeducted":100}`).
		Save(ctx)
	require.NoError(t, err)

	rollbackCalls := 0
	svc := &PaymentService{
		entClient:    client,
		loadBalancer: &captureLoadBalancer{},
		userRepo: &mockUserRepo{adjustBalanceFn: func(context.Context, int64, float64) (BalanceChange, error) {
			rollbackCalls++
			return BalanceChange{}, nil
		}},
	}
	restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{
		refundResponse: &payment.RefundResponse{RefundID: "rf_test", Status: payment.ProviderStatusFailed},
	})
	defer restore()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Success)
	require.Equal(t, 1, rollbackCalls)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundFailed, reloaded.Status)
	recovered, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_ROLLBACK_RECOVERED")).
		Only(ctx)
	require.NoError(t, err)
	require.Contains(t, recovered.Detail, `"refundID":"rf_test"`)
}

func TestQueryAndFinalizeRefundKeepsPendingWhenRollbackRecoveryFails(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "query-finalize-rollback-still-pending")
	pendingAudit, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
		Only(ctx)
	require.NoError(t, err)
	_, err = client.PaymentAuditLog.UpdateOneID(pendingAudit.ID).
		SetDetail(`{"refundID":"rf_test","refundAmount":100,"deductionRollbackOK":false,"deductionApplied":true,"balanceDeducted":100}`).
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient:    client,
		loadBalancer: &captureLoadBalancer{},
		userRepo: &mockUserRepo{adjustBalanceFn: func(context.Context, int64, float64) (BalanceChange, error) {
			return BalanceChange{}, errors.New("rollback unavailable")
		}},
	}
	restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{
		refundResponse: &payment.RefundResponse{RefundID: "rf_test", Status: payment.ProviderStatusFailed},
	})
	defer restore()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Success)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
	recoveryPending, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_ROLLBACK_PENDING")).
		Only(ctx)
	require.NoError(t, err)
	require.Contains(t, recoveryPending.Detail, `"refundID":"rf_test"`)
}

func TestQueryAndFinalizeRefundQueriesProviderBeforeFailingClosedOnMissingRollbackAmount(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "query-finalize-rollback-amount-missing")
	pendingAudit, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
		Only(ctx)
	require.NoError(t, err)
	_, err = client.PaymentAuditLog.UpdateOneID(pendingAudit.ID).
		SetDetail(`{"refundID":"rf_test","refundAmount":100,"deductionRollbackOK":false,"deductionApplied":true}`).
		Save(ctx)
	require.NoError(t, err)

	provider := &refundQueryProviderTestDouble{
		refundResponse: &payment.RefundResponse{RefundID: "rf_test", Status: payment.ProviderStatusFailed},
	}
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	restore := replacePaymentProviderFactoryForTest(t, provider)
	defer restore()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, "REFUND_ROLLBACK_UNRESOLVED", infraerrors.Reason(err))
	require.Len(t, provider.requests, 1, "the read-only provider result may prove that no rollback is needed")
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
}

func TestQueryAndFinalizeRefundFinalizesLegacyAttemptWithoutDeductionAmount(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "legacy-attempt-without-deduction-amount")
	_, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefunding).Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(order.ID, 10)).
		SetAction("REFUND_ATTEMPT").
		SetOperator("admin").
		SetDetail(`{"refundID":"rf_0123456789abcdef01234567","refundAmount":100,"deductionApplied":true}`).
		Save(ctx)
	require.NoError(t, err)

	provider := &refundQueryProviderTestDouble{
		refundResponse: &payment.RefundResponse{RefundID: "re_legacy_success", Status: payment.ProviderStatusSuccess},
	}
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	restore := replacePaymentProviderFactoryForTest(t, provider)
	defer restore()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Len(t, provider.requests, 1)
	require.Empty(t, provider.requests[0].RefundID)
	require.Empty(t, provider.requests[0].AttemptID)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunded, reloaded.Status)
}

func TestRefundAttemptCommitUnknownKeepsOrderRefunding(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "attempt-commit-unknown")
	_, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefunding).Save(ctx)
	require.NoError(t, err)
	original := *order
	original.Status = OrderStatusCompleted
	plan := &RefundPlan{
		OrderID:            order.ID,
		Order:              &original,
		ProviderRefundID:   "rf_attempt_commit_unknown",
		DeductionLineageID: "rf_attempt_commit_unknown",
		BalanceToDeduct:    100,
	}
	svc := &PaymentService{entClient: client}

	err = svc.handleRefundAttemptPreparationError(ctx, plan, &refundAttemptCommitOutcomeUnknownError{err: errors.New("commit acknowledgement lost")})
	require.Error(t, err)
	require.Equal(t, "REFUND_ATTEMPT_COMMIT_UNKNOWN", infraerrors.Reason(err))
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunding, reloaded.Status)
	audit, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_ATTEMPT_COMMIT_UNKNOWN")).
		Only(ctx)
	require.NoError(t, err)
	require.Contains(t, audit.Detail, `"deductionLineageID":"rf_attempt_commit_unknown"`)
}

func TestPersistRefundAttemptRollsBackWithProvidedTransaction(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "attempt-transaction")
	svc := &PaymentService{entClient: client}
	plan := &RefundPlan{
		OrderID:            order.ID,
		Order:              order,
		RefundAmount:       10,
		GatewayAmount:      10,
		DeductBalance:      true,
		DeductionType:      payment.DeductionTypeBalance,
		BalanceToDeduct:    10,
		ProviderRefundID:   "rf_attempt_transaction",
		DeductionLineageID: "rf_attempt_transaction",
	}

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(ctx, tx)
	require.NoError(t, svc.persistRefundAttempt(txCtx, tx.Client(), plan))
	require.NoError(t, tx.Rollback())

	count, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_ATTEMPT")).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count, "the attempt marker must share the entitlement transaction")
}

func TestHandleGatewayFailureCommitsRollbackAndRecoveryMarkerTogether(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "atomic-gateway-failure-rollback")
	_, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefunding).Save(ctx)
	require.NoError(t, err)
	original := *order
	original.Status = OrderStatusCompleted
	rollbackCalls := 0
	svc := &PaymentService{
		entClient: client,
		userRepo: &mockUserRepo{adjustBalanceFn: func(callCtx context.Context, _ int64, amount float64) (BalanceChange, error) {
			require.NotNil(t, dbent.TxFromContext(callCtx))
			require.Equal(t, 100.0, amount)
			rollbackCalls++
			return BalanceChange{}, nil
		}},
	}
	plan := &RefundPlan{
		OrderID:            order.ID,
		Order:              &original,
		RefundAmount:       100,
		ProviderRefundID:   "rf_atomic_rollback",
		DeductionLineageID: "rf_atomic_rollback",
		DeductionType:      payment.DeductionTypeBalance,
		BalanceToDeduct:    100,
	}

	result, err := svc.handleGwFail(ctx, plan, errors.New("provider rejected refund"), false)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Success)
	require.Equal(t, 1, rollbackCalls)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	recovered, err := refundRollbackWasRecoveredWithClient(ctx, client, order.ID, "rf_atomic_rollback")
	require.NoError(t, err)
	require.True(t, recovered)
	gatewayFailureAudits, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_GATEWAY_FAILED")).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, gatewayFailureAudits)
}

func TestTerminalGatewayFailureUsesFreshIdempotencyKeyOnRetry(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "terminal-gateway-failure-retry")
	_, err := client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefunding).Save(ctx)
	require.NoError(t, err)
	original := *order
	original.Status = OrderStatusCompleted
	initialAttemptID := refundProviderAttemptID(&RefundPlan{
		OrderID:       order.ID,
		Order:         &original,
		RefundAmount:  order.Amount,
		GatewayAmount: order.PayAmount,
	})

	svc := &PaymentService{entClient: client}
	result, err := svc.finishRefund(ctx, &RefundPlan{
		OrderID:            order.ID,
		Order:              &original,
		RefundAmount:       order.Amount,
		GatewayAmount:      order.PayAmount,
		ProviderRefundID:   initialAttemptID,
		DeductionLineageID: initialAttemptID,
	}, &payment.RefundResponse{RefundID: "provider-terminal-failure", Status: payment.ProviderStatusFailed})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Success)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundFailed, reloaded.Status)
	require.NotNil(t, reloaded.FailedAt)

	retryPlan, early, err := svc.PrepareRefund(ctx, order.ID, order.Amount, "retry terminal failure", false, false, 0)
	require.NoError(t, err)
	require.Nil(t, early)
	require.NotNil(t, retryPlan)
	require.NotEqual(t, initialAttemptID, retryPlan.ProviderRefundID)
}

func TestResolveLegacyRefundSubscriptionDetailUsesBoundOrderSubscription(t *testing.T) {
	groupID := int64(22)
	subscriptionID := int64(33)
	deletedAt := time.Now()
	repo := &refundLegacySubscriptionRepoStub{sub: &UserSubscription{
		ID:        subscriptionID,
		UserID:    11,
		GroupID:   groupID,
		DeletedAt: &deletedAt,
	}}
	svc := &PaymentService{subscriptionSvc: &SubscriptionService{userSubRepo: repo}}

	detail, err := svc.resolveLegacyRefundSubscriptionDetail(context.Background(), &dbent.PaymentOrder{
		ID:                      44,
		UserID:                  11,
		SubscriptionGroupID:     &groupID,
		FulfilledSubscriptionID: &subscriptionID,
	}, refundPendingAuditDetail{SubDaysDeducted: 7})
	require.NoError(t, err)
	require.Equal(t, subscriptionID, detail.SubscriptionID)
	require.True(t, detail.SubscriptionRevoked)
}

func TestLatestUnresolvedRefundRollbackMatchesEachTranche(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "rollback-tranche-match")
	orderID := strconv.FormatInt(order.ID, 10)
	_, err := client.PaymentAuditLog.Create().SetOrderID(orderID).SetAction("REFUND_ROLLBACK_FAILED").SetOperator("admin").
		SetDetail(`{"refundID":"refund-a","balanceDeducted":10,"deductionApplied":true}`).Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentAuditLog.Create().SetOrderID(orderID).SetAction("REFUND_ROLLBACK_FAILED").SetOperator("admin").
		SetDetail(`{"refundID":"refund-b","balanceDeducted":20,"deductionApplied":true}`).Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentAuditLog.Create().SetOrderID(orderID).SetAction("REFUND_SUCCESS").SetOperator("admin").
		SetDetail(`{"refundID":"refund-b","refundAmount":20}`).Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client}
	detail, ok, err := svc.latestUnresolvedRefundRollback(ctx, &RefundPlan{OrderID: order.ID, ProviderRefundID: "refund-a"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "refund-a", detail.RefundID)
	require.Equal(t, 10.0, detail.BalanceDeducted)
}

func TestExecuteRefundRetryCarriesUnresolvedDeductionLineage(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "retry-lineage")
	_, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetStatus(OrderStatusRefundFailed).
		Save(ctx)
	require.NoError(t, err)
	order, err = client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	_, err = client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(order.ID, 10)).
		SetAction("REFUND_ROLLBACK_FAILED").
		SetOperator("admin").
		SetDetail(`{"refundID":"refund-attempt-a","refundAmount":100,"previousRefundAmount":0,"balanceDeducted":100,"deductionApplied":true}`).
		Save(ctx)
	require.NoError(t, err)

	deductionCalls := 0
	svc := &PaymentService{
		entClient:    client,
		loadBalancer: &captureLoadBalancer{},
		userRepo: &mockUserRepo{deductAvailableBalanceFn: func(context.Context, int64, float64) (float64, error) {
			deductionCalls++
			return 100, nil
		}},
	}
	provider := &refundExecutionProviderTestDouble{
		refundResponse: &payment.RefundResponse{RefundID: "refund-attempt-b", Status: payment.ProviderStatusSuccess},
	}
	restore := replacePaymentProviderFactoryForTest(t, provider)
	defer restore()

	plan := &RefundPlan{
		OrderID:            order.ID,
		Order:              order,
		RefundAmount:       100,
		GatewayAmount:      100,
		Reason:             "retry unresolved deduction",
		Force:              true,
		DeductionType:      payment.DeductionTypeBalance,
		BalanceToDeduct:    100,
		ProviderRefundID:   "refund-attempt-b",
		DeductionLineageID: "refund-attempt-b",
	}

	result, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Equal(t, 100.0, result.BalanceDeducted)
	require.Zero(t, deductionCalls, "a retry must not deduct again while the prior rollback is unresolved")
	require.Equal(t, "refund-attempt-a", plan.DeductionLineageID)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefunded, reloaded.Status)
	success, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
		Only(ctx)
	require.NoError(t, err)
	require.Contains(t, success.Detail, `"deductionLineageID":"refund-attempt-a"`)
}

func TestExecuteRefundFailedRetryRecoversLegacyDeductionOnce(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "retry-legacy-rollback")
	_, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetStatus(OrderStatusRefundFailed).
		Save(ctx)
	require.NoError(t, err)
	order, err = client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	legacyFailure, err := client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(order.ID, 10)).
		SetAction("REFUND_ROLLBACK_FAILED").
		SetOperator("admin").
		SetDetail(`{"refundAmount":100,"balanceDeducted":100,"deductionApplied":true}`).
		Save(ctx)
	require.NoError(t, err)
	legacyLineage := legacyRefundRollbackLineage(legacyFailure.ID)

	deductionCalls := 0
	rollbackCalls := 0
	svc := &PaymentService{
		entClient:    client,
		loadBalancer: &captureLoadBalancer{},
		userRepo: &mockUserRepo{
			deductAvailableBalanceFn: func(context.Context, int64, float64) (float64, error) {
				deductionCalls++
				return 100, nil
			},
			adjustBalanceFn: func(callCtx context.Context, _ int64, amount float64) (BalanceChange, error) {
				require.NotNil(t, dbent.TxFromContext(callCtx))
				require.Equal(t, 100.0, amount)
				rollbackCalls++
				return BalanceChange{}, nil
			},
		},
	}
	provider := &refundExecutionProviderTestDouble{
		refundResponse: &payment.RefundResponse{RefundID: "refund-retry-failed", Status: payment.ProviderStatusFailed},
	}
	restore := replacePaymentProviderFactoryForTest(t, provider)
	defer restore()

	plan := &RefundPlan{
		OrderID:            order.ID,
		Order:              order,
		RefundAmount:       100,
		GatewayAmount:      100,
		Reason:             "retry legacy unresolved deduction",
		Force:              true,
		DeductionType:      payment.DeductionTypeBalance,
		BalanceToDeduct:    100,
		ProviderRefundID:   "refund-retry-attempt",
		DeductionLineageID: "refund-retry-attempt",
	}

	result, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Success)
	require.Zero(t, deductionCalls, "the inherited deduction must not be applied again")
	require.Equal(t, 1, rollbackCalls)
	require.Equal(t, legacyLineage, plan.DeductionLineageID)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundFailed, reloaded.Status)
	recovered, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_ROLLBACK_RECOVERED")).
		Only(ctx)
	require.NoError(t, err)
	require.Contains(t, recovered.Detail, `"deductionLineageID":"`+legacyLineage+`"`)

	_, unresolved, err := svc.latestUnresolvedRefundRollback(ctx, &RefundPlan{
		OrderID:            order.ID,
		Order:              reloaded,
		RefundAmount:       100,
		GatewayAmount:      100,
		BalanceToDeduct:    100,
		ProviderRefundID:   "another-refund-attempt",
		DeductionLineageID: "another-refund-attempt",
	})
	require.NoError(t, err)
	require.False(t, unresolved, "a recovered legacy deduction must not be reusable by another equal refund")
}

func TestPendingRefundPreservesExplicitNoDeductionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		orderType string
	}{
		{name: "balance", orderType: payment.OrderTypeBalance},
		{name: "subscription", orderType: payment.OrderTypeSubscription},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "no-deduction-"+tc.name)
			_, err := client.PaymentAuditLog.Delete().
				Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10))).
				Exec(ctx)
			require.NoError(t, err)
			_, err = client.PaymentOrder.UpdateOneID(order.ID).
				SetStatus(OrderStatusCompleted).
				SetOrderType(tc.orderType).
				Save(ctx)
			require.NoError(t, err)
			order, err = client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)

			deductionCalls := 0
			provider := &refundExecutionProviderTestDouble{
				refundResponse: &payment.RefundResponse{RefundID: "re_no_deduction_" + tc.name, Status: payment.ProviderStatusPending},
			}
			svc := &PaymentService{
				entClient:    client,
				loadBalancer: &captureLoadBalancer{},
				userRepo: &mockUserRepo{deductAvailableBalanceFn: func(context.Context, int64, float64) (float64, error) {
					deductionCalls++
					return 0, nil
				}},
			}
			restore := replacePaymentProviderFactoryForTest(t, provider)
			defer restore()

			plan, early, err := svc.PrepareRefund(ctx, order.ID, 100, "refund without entitlement deduction", false, false, 7)
			require.NoError(t, err)
			require.Nil(t, early)
			require.NotNil(t, plan)
			require.False(t, plan.DeductBalance)
			require.Equal(t, payment.DeductionTypeNone, plan.DeductionType)
			require.Zero(t, plan.SubDaysToDeduct, "a stale UI suggestion must not override the cleared deduction checkbox")
			pending, err := svc.ExecuteRefund(ctx, plan)
			require.NoError(t, err)
			require.NotNil(t, pending)
			require.False(t, pending.Success)
			require.Zero(t, deductionCalls)

			pendingAudit, err := client.PaymentAuditLog.Query().
				Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
				Only(ctx)
			require.NoError(t, err)
			require.Contains(t, pendingAudit.Detail, `"deductionRequested":false`)
			require.Contains(t, pendingAudit.Detail, `"deductionType":"none"`)

			provider.refundResponse = &payment.RefundResponse{RefundID: "re_no_deduction_" + tc.name, Status: payment.ProviderStatusSuccess}
			result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.True(t, result.Success)
			require.Zero(t, deductionCalls, "provider confirmation must preserve the administrator's no-deduction choice")

			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusRefunded, reloaded.Status)
		})
	}
}

func TestFailedRefundRetryUsesFreshLineageForRollbackRecovery(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "fresh-retry-lineage")
	orderID := strconv.FormatInt(order.ID, 10)
	_, err := client.PaymentAuditLog.Delete().Where(paymentauditlog.OrderIDEQ(orderID)).Exec(ctx)
	require.NoError(t, err)
	_, err = client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefundFailed).Save(ctx)
	require.NoError(t, err)
	order, err = client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)

	oldLineage := refundProviderAttemptID(&RefundPlan{
		OrderID:              order.ID,
		Order:                order,
		RefundAmount:         100,
		PreviousRefundAmount: 0,
		GatewayAmount:        100,
	})
	_, err = client.PaymentAuditLog.Create().
		SetOrderID(orderID).
		SetAction("REFUND_ROLLBACK_RECOVERED").
		SetOperator("admin").
		SetDetail(`{"deductionLineageID":"` + oldLineage + `","balanceRolledBack":100}`).
		Save(ctx)
	require.NoError(t, err)

	rollbackCalls := 0
	provider := &refundExecutionProviderTestDouble{
		refundResponse: &payment.RefundResponse{RefundID: "re_fresh_retry", Status: payment.ProviderStatusPending},
	}
	svc := &PaymentService{
		entClient:    client,
		loadBalancer: &captureLoadBalancer{},
		userRepo: &mockUserRepo{
			getByIDUser: &User{ID: order.UserID, Balance: 100},
			deductAvailableBalanceFn: func(context.Context, int64, float64) (float64, error) {
				return 100, nil
			},
			adjustBalanceFn: func(context.Context, int64, float64) (BalanceChange, error) {
				rollbackCalls++
				if rollbackCalls == 1 {
					return BalanceChange{}, errors.New("temporary rollback failure")
				}
				return BalanceChange{}, nil
			},
		},
	}
	restore := replacePaymentProviderFactoryForTest(t, provider)
	defer restore()

	plan, early, err := svc.PrepareRefund(ctx, order.ID, 100, "retry with fresh lineage", true, true, 0)
	require.NoError(t, err)
	require.Nil(t, early)
	require.NotNil(t, plan)
	require.NotEqual(t, oldLineage, plan.ProviderRefundID)
	require.Equal(t, plan.ProviderRefundID, plan.DeductionLineageID)

	pending, err := svc.ExecuteRefund(ctx, plan)
	require.NoError(t, err)
	require.NotNil(t, pending)
	require.False(t, pending.Success)
	require.Equal(t, 1, rollbackCalls)

	provider.refundResponse = &payment.RefundResponse{RefundID: "re_fresh_retry", Status: payment.ProviderStatusFailed}
	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Success)
	require.Equal(t, 2, rollbackCalls, "an older recovered lineage must not suppress compensation for the new attempt")

	recovered, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(orderID), paymentauditlog.ActionEQ("REFUND_ROLLBACK_RECOVERED")).
		Order(paymentauditlog.ByCreatedAt()).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, recovered, 2)
	require.Contains(t, recovered[1].Detail, `"deductionLineageID":"`+plan.DeductionLineageID+`"`)
}

func TestQueryAndFinalizeRefundFailsClosedWhenDeductionStateIsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, ctx context.Context, client *dbent.Client, orderID int64)
	}{
		{
			name: "missing audit",
			mutate: func(t *testing.T, ctx context.Context, client *dbent.Client, orderID int64) {
				_, err := client.PaymentAuditLog.Delete().
					Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(orderID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
					Exec(ctx)
				require.NoError(t, err)
			},
		},
		{
			name: "malformed audit",
			mutate: func(t *testing.T, ctx context.Context, client *dbent.Client, orderID int64) {
				audit, err := client.PaymentAuditLog.Query().
					Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(orderID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
					Only(ctx)
				require.NoError(t, err)
				_, err = client.PaymentAuditLog.UpdateOneID(audit.ID).SetDetail("{").Save(ctx)
				require.NoError(t, err)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			order := createPendingRefundOrderForTest(t, ctx, client, "unknown-deduction-"+tc.name)
			tc.mutate(t, ctx, client, order.ID)

			provider := &refundQueryProviderTestDouble{
				refundResponse: &payment.RefundResponse{RefundID: "re_unknown_state", Status: payment.ProviderStatusSuccess},
			}
			deductionCalls := 0
			svc := &PaymentService{
				entClient:    client,
				loadBalancer: &captureLoadBalancer{},
				userRepo: &mockUserRepo{deductAvailableBalanceFn: func(context.Context, int64, float64) (float64, error) {
					deductionCalls++
					return 100, nil
				}},
			}
			restore := replacePaymentProviderFactoryForTest(t, provider)
			defer restore()

			result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
			require.Nil(t, result)
			require.Error(t, err)
			require.Equal(t, "REFUND_DEDUCTION_STATE_UNKNOWN", infraerrors.Reason(err))
			require.Len(t, provider.requests, 1, "the provider query remains read-only and actionable")
			require.Zero(t, deductionCalls)

			reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusRefundPending, reloaded.Status)
			unknownAudits, err := client.PaymentAuditLog.Query().
				Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_QUERY_DEDUCTION_STATE_UNKNOWN")).
				Count(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, unknownAudits)
		})
	}
}

func TestRefundPendingDetailUsesAppliedStateForNewAuditRows(t *testing.T) {
	requested := true
	notRequested := false
	for _, tc := range []struct {
		name   string
		detail refundPendingAuditDetail
		want   bool
	}{
		{
			name: "explicit no deduction",
			detail: refundPendingAuditDetail{
				DeductionStateKnown: true,
				DeductionRequested:  &notRequested,
				DeductionRollbackOK: false,
			},
			want: false,
		},
		{
			name: "requested but clamped to zero",
			detail: refundPendingAuditDetail{
				DeductionStateKnown: true,
				DeductionRequested:  &requested,
				DeductionApplied:    false,
				DeductionRollbackOK: false,
			},
			want: false,
		},
		{
			name: "new applied deduction",
			detail: refundPendingAuditDetail{
				DeductionStateKnown: true,
				DeductionRequested:  &requested,
				DeductionApplied:    true,
				DeductionRollbackOK: false,
			},
			want: true,
		},
		{
			name:   "legacy unresolved rollback",
			detail: refundPendingAuditDetail{DeductionRollbackOK: false},
			want:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.detail.deductionNeedsRollbackRecovery())
		})
	}
}

func TestQueryAndFinalizeRefundDoesNotRedeductBalanceAfterRollbackFailure(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "rollback-failed-balance")
	pendingAudit, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
		Only(ctx)
	require.NoError(t, err)
	_, err = client.PaymentAuditLog.UpdateOneID(pendingAudit.ID).
		SetDetail(`{"refundID":"rf_test","refundAmount":100,"deductionRollbackOK":false,"deductionApplied":true,"balanceDeducted":100}`).
		Save(ctx)
	require.NoError(t, err)

	deductionCalls := 0
	svc := &PaymentService{
		entClient:    client,
		loadBalancer: &captureLoadBalancer{},
		userRepo: &mockUserRepo{deductAvailableBalanceFn: func(context.Context, int64, float64) (float64, error) {
			deductionCalls++
			return 100, nil
		}},
	}
	restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{
		refundResponse: &payment.RefundResponse{RefundID: "rf_test", Status: payment.ProviderStatusSuccess},
	})
	defer restore()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 100.0, result.BalanceDeducted)
	require.Zero(t, deductionCalls, "a deduction whose rollback failed must not be applied again")
}

func TestQueryAndFinalizeRefundDoesNotRedeductSubscriptionAfterRollbackFailure(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "rollback-failed-subscription")
	_, err := client.PaymentOrder.UpdateOneID(order.ID).SetOrderType(payment.OrderTypeSubscription).Save(ctx)
	require.NoError(t, err)
	pendingAudit, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_PENDING")).
		Only(ctx)
	require.NoError(t, err)
	_, err = client.PaymentAuditLog.UpdateOneID(pendingAudit.ID).
		SetDetail(`{"refundID":"rf_test","refundAmount":100,"deductionRollbackOK":false,"deductionApplied":true,"subDaysDeducted":7,"subscriptionID":123}`).
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	restore := replacePaymentProviderFactoryForTest(t, &refundQueryProviderTestDouble{
		refundResponse: &payment.RefundResponse{RefundID: "rf_test", Status: payment.ProviderStatusSuccess},
	})
	defer restore()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 7, result.SubDaysDeducted)
}

func TestQueryAndFinalizeRefundKeepsCumulativeAmountForPartialPending(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("query-finalize-partial-pending@example.com").
		SetPasswordHash("hash").
		SetUsername("query-finalize-partial-pending").
		Save(ctx)
	require.NoError(t, err)
	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeStripe).
		SetName("query-finalize-partial-pending-provider").
		SetConfig("{}").
		SetSupportedTypes("stripe").
		SetEnabled(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode("REFUND-PARTIAL-PENDING").
		SetOutTradeNo("sub2_refund_partial_pending").
		SetPaymentType(payment.TypeStripe).
		SetPaymentTradeNo("pi_refund_partial_pending").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusPartiallyRefunded).
		SetRefundAmount(30).
		SetRefundReason("first partial refund").
		SetRefundAt(time.Now()).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(strconv.FormatInt(inst.ID, 10)).
		Save(ctx)
	require.NoError(t, err)

	var rolledBack float64
	var deducted float64
	svc := &PaymentService{
		entClient:    client,
		loadBalancer: &captureLoadBalancer{},
		userRepo: &mockUserRepo{
			adjustBalanceFn: func(ctx context.Context, id int64, amount float64) (BalanceChange, error) {
				require.Equal(t, user.ID, id)
				rolledBack += amount
				return BalanceChange{}, nil
			},
			deductAvailableBalanceFn: func(ctx context.Context, id int64, amount float64) (float64, error) {
				require.Equal(t, user.ID, id)
				deducted += amount
				return amount, nil
			},
		},
	}
	plan := &RefundPlan{
		OrderID:              order.ID,
		Order:                order,
		RefundAmount:         20,
		PreviousRefundAmount: 30,
		GatewayAmount:        20,
		Reason:               "second partial refund",
		Force:                true,
		DeductionType:        payment.DeductionTypeBalance,
		BalanceToDeduct:      20,
	}
	// finishRefund is invoked only after ExecuteRefund has atomically claimed
	// the order. Model that production state so the status CAS is exercised.
	_, err = client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusRefunding).Save(ctx)
	require.NoError(t, err)
	order.Status = OrderStatusRefunding

	pendingResult, err := svc.finishRefund(ctx, plan, &payment.RefundResponse{RefundID: "rf_second_partial", Status: payment.ProviderStatusPending})
	require.NoError(t, err)
	require.NotNil(t, pendingResult)
	require.False(t, pendingResult.Success)
	require.Equal(t, 20.0, rolledBack)
	pendingOrder, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, pendingOrder.Status)
	require.Equal(t, 20.0, pendingOrder.RefundAmount)

	provider := &refundQueryProviderTestDouble{
		refundResponse: &payment.RefundResponse{RefundID: "rf_second_partial", Status: payment.ProviderStatusSuccess},
	}
	restore := replacePaymentProviderFactoryForTest(t, provider)
	defer restore()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)
	require.Equal(t, 20.0, deducted)
	require.Len(t, provider.requests, 1)
	require.Equal(t, formatGatewayRefundAmount(20, pendingOrder), provider.requests[0].Amount)

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPartiallyRefunded, reloaded.Status)
	require.Equal(t, 50.0, reloaded.RefundAmount)
	successAudit, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
		Only(ctx)
	require.NoError(t, err)
	require.Contains(t, successAudit.Detail, `"refundAmount":20`)
	require.Contains(t, successAudit.Detail, `"previousRefundAmount":30`)
	require.Contains(t, successAudit.Detail, `"totalRefunded":50`)
}

func TestFinalizePendingRefundSuccessRejectsStaleCallerBeforeSecondDeduction(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "finalize-stale")

	deductions := 0
	svc := &PaymentService{
		entClient: client,
		userRepo: &mockUserRepo{deductAvailableBalanceFn: func(ctx context.Context, id int64, amount float64) (float64, error) {
			require.NotNil(t, dbent.TxFromContext(ctx))
			deductions++
			return amount, nil
		}},
	}

	pendingDetail := svc.latestRefundPendingDetail(ctx, order.ID)
	first, err := svc.finalizeRefundSuccessFromStatus(ctx, svc.refundFinalizePlan(order, pendingDetail), OrderStatusRefundPending)
	require.NoError(t, err)
	require.True(t, first.Success)

	second, err := svc.finalizeRefundSuccessFromStatus(ctx, svc.refundFinalizePlan(order, pendingDetail), OrderStatusRefundPending)
	require.Nil(t, second)
	require.Error(t, err)
	require.Equal(t, "CONFLICT", infraerrors.Reason(err))
	require.Equal(t, 1, deductions)

	successAudits, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, successAudits)
}

func TestFinalizePendingRefundSuccessRollsBackPostDeductionFailure(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "finalize-rollback")
	_, err := client.User.UpdateOneID(order.UserID).SetBalance(100).Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{
		entClient: client,
		userRepo: &mockUserRepo{deductAvailableBalanceFn: func(ctx context.Context, id int64, amount float64) (float64, error) {
			tx := dbent.TxFromContext(ctx)
			require.NotNil(t, tx)
			if _, updateErr := tx.Client().User.UpdateOneID(id).AddBalance(-amount).Save(ctx); updateErr != nil {
				return 0, updateErr
			}
			return 0, errors.New("injected failure after deduction")
		}},
	}

	result, err := svc.finalizeRefundSuccessFromStatus(ctx, svc.refundFinalizePlan(order, svc.latestRefundPendingDetail(ctx, order.ID)), OrderStatusRefundPending)
	require.Nil(t, result)
	require.ErrorContains(t, err, "injected failure after deduction")

	user, err := client.User.Get(ctx, order.UserID)
	require.NoError(t, err)
	require.Equal(t, 100.0, user.Balance)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
	successAudits, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(order.ID, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, successAudits)
}

func TestQueryAndFinalizeRefundUnsupportedProviderReturnsClearError(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPendingRefundOrderForTest(t, ctx, client, "query-finalize-unsupported")
	svc := &PaymentService{entClient: client, loadBalancer: &captureLoadBalancer{}}
	restore := replacePaymentProviderFactoryForTest(t, refundProviderTestDouble{})
	defer restore()

	result, err := svc.QueryAndFinalizeRefund(ctx, order.ID)
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, "REFUND_QUERY_UNSUPPORTED", infraerrors.Reason(err))
}

func createPendingRefundOrderForTest(t *testing.T, ctx context.Context, client *dbent.Client, suffix string) *dbent.PaymentOrder {
	t.Helper()

	user, err := client.User.Create().
		SetEmail(suffix + "@example.com").
		SetPasswordHash("hash").
		SetUsername(suffix).
		Save(ctx)
	require.NoError(t, err)

	inst, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeStripe).
		SetName(suffix + "-provider").
		SetConfig("{}").
		SetSupportedTypes("stripe").
		SetEnabled(true).
		SetRefundEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode("REFUND-" + suffix).
		SetOutTradeNo("sub2_" + suffix).
		SetPaymentType(payment.TypeStripe).
		SetPaymentTradeNo("pi_" + suffix).
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusRefundPending).
		SetRefundAmount(100).
		SetRefundReason("pending refund").
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now()).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(strconv.FormatInt(inst.ID, 10)).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(order.ID, 10)).
		SetAction("REFUND_PENDING").
		SetOperator("admin").
		SetDetail(`{"refundID":"rf_test","deductionRollbackOK":true}`).
		Save(ctx)
	require.NoError(t, err)
	return order
}

func replacePaymentProviderFactoryForTest(t *testing.T, prov payment.Provider) func() {
	t.Helper()
	original := createPaymentProviderFromInstance
	createPaymentProviderFromInstance = func(providerKey, instanceID string, config map[string]string) (payment.Provider, error) {
		return prov, nil
	}
	return func() { createPaymentProviderFromInstance = original }
}

type refundProviderTestDouble struct{}

func (refundProviderTestDouble) Name() string { return "refund-test" }
func (refundProviderTestDouble) ProviderKey() string {
	return payment.TypeStripe
}
func (refundProviderTestDouble) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeStripe}
}
func (refundProviderTestDouble) CreatePayment(context.Context, payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	return nil, nil
}
func (refundProviderTestDouble) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	return nil, nil
}
func (refundProviderTestDouble) VerifyNotification(context.Context, string, map[string]string) (*payment.PaymentNotification, error) {
	return nil, nil
}
func (refundProviderTestDouble) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	return nil, nil
}

type refundExecutionProviderTestDouble struct {
	refundProviderTestDouble
	refundResponse *payment.RefundResponse
	refundErr      error
}

func (p *refundExecutionProviderTestDouble) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	return p.refundResponse, p.refundErr
}

func (p *refundExecutionProviderTestDouble) QueryRefund(context.Context, payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	return p.refundResponse, p.refundErr
}

type refundQueryProviderTestDouble struct {
	refundProviderTestDouble
	refundResponse *payment.RefundResponse
	requests       []payment.RefundQueryRequest
}

func (p *refundQueryProviderTestDouble) QueryRefund(_ context.Context, req payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	p.requests = append(p.requests, req)
	return p.refundResponse, nil
}

func (p *refundQueryProviderTestDouble) QueryRefundByOrder(_ context.Context, req payment.RefundQueryRequest) (*payment.RefundResponse, error) {
	p.requests = append(p.requests, req)
	return p.refundResponse, nil
}

type refundLegacySubscriptionRepoStub struct {
	userSubRepoNoop
	sub *UserSubscription
}

func (r *refundLegacySubscriptionRepoStub) GetByIDIncludeDeleted(_ context.Context, id int64) (*UserSubscription, error) {
	if r.sub == nil || r.sub.ID != id {
		return nil, ErrSubscriptionNotFound
	}
	clone := *r.sub
	return &clone, nil
}
