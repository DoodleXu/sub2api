//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionUpgradePayableHasMinimum(t *testing.T) {
	require.Equal(t, 0.01, subscriptionUpgradePayableAmount(100, 100))
	require.Equal(t, 99.99, subscriptionUpgradeCreditAmount(100, 200))
	require.Equal(t, 12.35, subscriptionUpgradeCreditAmount(100, 12.345))
}

func TestDaysRemainingFromNowCeilsPartialDays(t *testing.T) {
	require.Equal(t, 2, daysRemainingFromNow(time.Now().Add(25*time.Hour)))
	require.Equal(t, 1, daysRemainingFromNow(time.Now().Add(1*time.Minute)))
	require.Equal(t, 0, daysRemainingFromNow(time.Now().Add(-time.Minute)))
}

func TestCalculateSubscriptionUpgradeCreditRejectsCrossGroup(t *testing.T) {
	svc := &PaymentService{}
	_, err := svc.calculateSubscriptionUpgradeCredit(context.Background(), 1, &UserSubscription{
		UserID:    1,
		GroupID:   10,
		Status:    SubscriptionStatusActive,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}, &dbent.SubscriptionPlan{ID: 2, GroupID: 20, Price: 100})

	require.Error(t, err)
	require.Equal(t, "UPGRADE_GROUP_MISMATCH", infraerrors.Reason(err))
}

func TestCalculateSubscriptionUpgradeCreditUsesRemainingDaysAndMinimumPayable(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("upgrade@example.com").
		SetPasswordHash("hash").
		SetUsername("upgrade-user").
		Save(ctx)
	require.NoError(t, err)
	group, err := client.Group.Create().
		SetName("upgrade-group").
		SetSubscriptionType(SubscriptionTypeSubscription).
		SetStatus(StatusActive).
		Save(ctx)
	require.NoError(t, err)

	days := 30
	_, err = client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail("upgrade@example.com").
		SetUserName("upgrade-user").
		SetAmount(300).
		SetPayAmount(300).
		SetFeeRate(0).
		SetRechargeCode("UPGRADE-CREDIT-ORDER").
		SetOutTradeNo("sub2_upgrade_credit_order").
		SetPaymentType("alipay").
		SetPaymentTradeNo("trade-upgrade-credit").
		SetOrderType("subscription").
		SetPlanID(100).
		SetSubscriptionGroupID(group.ID).
		SetSubscriptionDays(days).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetPaidAt(time.Now().Add(-10 * 24 * time.Hour)).
		SetCompletedAt(time.Now().Add(-10 * 24 * time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client}
	credit, err := svc.calculateSubscriptionUpgradeCredit(ctx, user.ID, &UserSubscription{
		ID:        55,
		UserID:    user.ID,
		GroupID:   group.ID,
		Status:    SubscriptionStatusActive,
		ExpiresAt: time.Now().Add(20 * 24 * time.Hour),
	}, &dbent.SubscriptionPlan{ID: 200, GroupID: group.ID, Price: 100})

	require.NoError(t, err)
	require.Equal(t, int64(55), credit.SubscriptionID)
	require.Equal(t, 99.99, credit.CreditAmount)
	require.GreaterOrEqual(t, credit.CreditDays, 19)
	require.LessOrEqual(t, credit.CreditDays, 20)

	order, err := client.PaymentOrder.Query().Where(paymentorder.UserIDEQ(user.ID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, 300.0, order.Amount)
}

func TestCalculateSubscriptionUpgradeCreditUsesNetPaidAmountForPartialRefund(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("upgrade-refund@example.com").
		SetPasswordHash("hash").
		SetUsername("upgrade-refund-user").
		Save(ctx)
	require.NoError(t, err)
	group, err := client.Group.Create().
		SetName("upgrade-refund-group").
		SetSubscriptionType(SubscriptionTypeSubscription).
		SetStatus(StatusActive).
		Save(ctx)
	require.NoError(t, err)

	days := 30
	now := time.Now()
	_, err = client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail("upgrade-refund@example.com").
		SetUserName("upgrade-refund-user").
		SetAmount(300).
		SetPayAmount(300).
		SetFeeRate(0).
		SetRefundAmount(150).
		SetRechargeCode("UPGRADE-REFUND-ORDER").
		SetOutTradeNo("sub2_upgrade_refund_order").
		SetPaymentType("alipay").
		SetPaymentTradeNo("trade-upgrade-refund").
		SetOrderType("subscription").
		SetPlanID(100).
		SetSubscriptionGroupID(group.ID).
		SetSubscriptionDays(days).
		SetStatus(OrderStatusPartiallyRefunded).
		SetExpiresAt(now.Add(time.Hour)).
		SetPaidAt(now.Add(-10 * 24 * time.Hour)).
		SetCompletedAt(now.Add(-10 * 24 * time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client}
	credit, err := svc.calculateSubscriptionUpgradeCredit(ctx, user.ID, &UserSubscription{
		ID:        56,
		UserID:    user.ID,
		GroupID:   group.ID,
		StartsAt:  now.Add(-10 * 24 * time.Hour),
		Status:    SubscriptionStatusActive,
		ExpiresAt: now.Add(10 * 24 * time.Hour),
	}, &dbent.SubscriptionPlan{ID: 200, GroupID: group.ID, Price: 100})

	require.NoError(t, err)
	require.InDelta(t, 50.0, credit.CreditAmount, 0.01)
}

func TestCalculateSubscriptionUpgradeCreditAggregatesCurrentTermOrders(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("upgrade-multi@example.com").
		SetPasswordHash("hash").
		SetUsername("upgrade-multi-user").
		Save(ctx)
	require.NoError(t, err)
	group, err := client.Group.Create().
		SetName("upgrade-multi-group").
		SetSubscriptionType(SubscriptionTypeSubscription).
		SetStatus(StatusActive).
		Save(ctx)
	require.NoError(t, err)

	now := time.Now()
	start := now.Add(-20 * 24 * time.Hour)
	makeOrder := func(outTradeNo string, amount float64, days int, createdAt time.Time) {
		t.Helper()
		_, err := client.PaymentOrder.Create().
			SetUserID(user.ID).
			SetUserEmail("upgrade-multi@example.com").
			SetUserName("upgrade-multi-user").
			SetAmount(amount).
			SetPayAmount(amount).
			SetFeeRate(0).
			SetRechargeCode(outTradeNo).
			SetOutTradeNo(outTradeNo).
			SetPaymentType("alipay").
			SetPaymentTradeNo("trade-" + outTradeNo).
			SetOrderType("subscription").
			SetPlanID(100).
			SetSubscriptionGroupID(group.ID).
			SetSubscriptionDays(days).
			SetStatus(OrderStatusCompleted).
			SetExpiresAt(now.Add(time.Hour)).
			SetPaidAt(createdAt).
			SetCompletedAt(createdAt).
			SetCreatedAt(createdAt).
			SetClientIP("127.0.0.1").
			SetSrcHost("api.example.com").
			Save(ctx)
		require.NoError(t, err)
	}
	makeOrder("sub2_previous_term", 900, 30, start.Add(-48*time.Hour))
	makeOrder("sub2_current_term_1", 100, 10, start.Add(time.Hour))
	makeOrder("sub2_current_term_2", 500, 50, start.Add(24*time.Hour))

	svc := &PaymentService{entClient: client}
	credit, err := svc.calculateSubscriptionUpgradeCredit(ctx, user.ID, &UserSubscription{
		ID:        57,
		UserID:    user.ID,
		GroupID:   group.ID,
		StartsAt:  start,
		Status:    SubscriptionStatusActive,
		ExpiresAt: now.Add(30 * 24 * time.Hour),
	}, &dbent.SubscriptionPlan{ID: 200, GroupID: group.ID, Price: 1000})

	require.NoError(t, err)
	require.InDelta(t, 300.0, credit.CreditAmount, 0.01)
}

func TestCreateOrderAppliesSubscriptionUpgradeCreditAndMinimumPayable(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)

	user, err := client.User.Create().
		SetEmail("upgrade-create@example.com").
		SetPasswordHash("hash").
		SetUsername("upgrade-create-user").
		Save(ctx)
	require.NoError(t, err)
	group, err := client.Group.Create().
		SetName("upgrade-create-group").
		SetSubscriptionType(SubscriptionTypeSubscription).
		SetStatus(StatusActive).
		Save(ctx)
	require.NoError(t, err)
	plan, err := client.SubscriptionPlan.Create().
		SetName("target").
		SetProductName("Target Plan").
		SetGroupID(group.ID).
		SetPrice(100).
		SetValidityDays(30).
		SetForSale(true).
		Save(ctx)
	require.NoError(t, err)

	now := time.Now()
	_, err = client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(300).
		SetPayAmount(300).
		SetFeeRate(0).
		SetRechargeCode("UPGRADE-CREATE-SOURCE").
		SetOutTradeNo("sub2_upgrade_create_source").
		SetPaymentType(payment.TypeEasyPay).
		SetPaymentTradeNo("trade-upgrade-create-source").
		SetOrderType(payment.OrderTypeSubscription).
		SetPlanID(1).
		SetSubscriptionGroupID(group.ID).
		SetSubscriptionDays(30).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(now.Add(time.Hour)).
		SetPaidAt(now.Add(-10 * 24 * time.Hour)).
		SetCompletedAt(now.Add(-10 * 24 * time.Hour)).
		SetCreatedAt(now.Add(-10 * 24 * time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	_, err = client.PaymentProviderInstance.Create().
		SetName("easypay").
		SetProviderKey(payment.TypeEasyPay).
		SetConfig(`{"apiBase":"https://pay.example.com","notifyUrl":"https://api.example.com/notify","returnUrl":"https://api.example.com/return","pid":"1000","pkey":"secret","paymentMode":"popup"}`).
		SetSupportedTypes(payment.TypeEasyPay).
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	configSvc := NewPaymentConfigService(client, &paymentConfigSettingRepoStub{values: map[string]string{
		SettingPaymentEnabled: "true",
	}}, nil)
	subscriptionSvc := &SubscriptionService{userSubRepo: paymentUpgradeSubscriptionRepoStub{
		byID: map[int64]*UserSubscription{
			77: {
				ID:        77,
				UserID:    user.ID,
				GroupID:   group.ID,
				StartsAt:  now.Add(-10 * 24 * time.Hour),
				Status:    SubscriptionStatusActive,
				ExpiresAt: now.Add(20 * 24 * time.Hour),
			},
		},
	}}
	svc := NewPaymentService(
		client,
		payment.NewRegistry(),
		payment.NewDefaultLoadBalancer(client, nil),
		nil,
		subscriptionSvc,
		configSvc,
		paymentUpgradeUserRepoStub{user: &User{ID: user.ID, Email: user.Email, Username: user.Username, Status: StatusActive}},
		paymentUpgradeGroupRepoStub{group: &Group{ID: group.ID, Status: StatusActive, SubscriptionType: SubscriptionTypeSubscription}},
		nil,
	)

	resp, err := svc.CreateOrder(ctx, CreateOrderRequest{
		UserID:                    user.ID,
		PaymentType:               payment.TypeEasyPay,
		OrderType:                 payment.OrderTypeSubscription,
		PlanID:                    plan.ID,
		UpgradeFromSubscriptionID: 77,
		ClientIP:                  "127.0.0.1",
		SrcHost:                   "api.example.com",
	})
	require.NoError(t, err)
	require.Equal(t, 0.01, resp.Amount)
	require.Equal(t, 0.01, resp.PayAmount)

	order, err := client.PaymentOrder.Query().Where(paymentorder.IDEQ(resp.OrderID)).Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, order.UpgradeFromSubscriptionID)
	require.Equal(t, int64(77), *order.UpgradeFromSubscriptionID)
	require.Equal(t, 99.99, order.UpgradeCreditAmount)
	require.NotNil(t, order.UpgradeCreditDays)
	require.GreaterOrEqual(t, *order.UpgradeCreditDays, 19)
	require.LessOrEqual(t, *order.UpgradeCreditDays, 20)
	require.Equal(t, 0.01, order.Amount)
	require.Equal(t, 0.01, order.PayAmount)
}

type paymentUpgradeUserRepoStub struct {
	UserRepository
	user *User
}

func (r paymentUpgradeUserRepoStub) GetByID(context.Context, int64) (*User, error) {
	if r.user == nil {
		return nil, ErrUserNotFound
	}
	return r.user, nil
}

type paymentUpgradeGroupRepoStub struct {
	GroupRepository
	group *Group
}

func (r paymentUpgradeGroupRepoStub) GetByID(context.Context, int64) (*Group, error) {
	if r.group == nil {
		return nil, ErrGroupNotFound
	}
	return r.group, nil
}

type paymentUpgradeSubscriptionRepoStub struct {
	UserSubscriptionRepository
	byID map[int64]*UserSubscription
}

func (r paymentUpgradeSubscriptionRepoStub) GetByID(_ context.Context, id int64) (*UserSubscription, error) {
	sub := r.byID[id]
	if sub == nil {
		return nil, ErrSubscriptionNotFound
	}
	clone := *sub
	return &clone, nil
}
