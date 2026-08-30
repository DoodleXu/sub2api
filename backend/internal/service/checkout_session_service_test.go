//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type checkoutOrderStub struct {
	created *CreateOrderResponse
	order   *dbent.PaymentOrder
	req     CreateOrderRequest
}

func (s *checkoutOrderStub) CreateOrder(_ context.Context, req CreateOrderRequest) (*CreateOrderResponse, error) {
	s.req = req
	return s.created, nil
}

func (s *checkoutOrderStub) GetOrder(_ context.Context, orderID, userID int64) (*dbent.PaymentOrder, error) {
	if s.order == nil || s.order.ID != orderID || s.order.UserID != userID {
		return nil, errors.New("order not found")
	}
	return s.order, nil
}

// checkoutOrderFuncStub lets concurrency/idempotency tests control the exact
// point at which the provider call returns without involving a real payment
// adapter.
type checkoutOrderFuncStub struct {
	mu       sync.Mutex
	createFn func(CreateOrderRequest) (*CreateOrderResponse, error)
	order    *dbent.PaymentOrder
	calls    int
}

func (s *checkoutOrderFuncStub) CreateOrder(_ context.Context, req CreateOrderRequest) (*CreateOrderResponse, error) {
	s.mu.Lock()
	s.calls++
	createFn := s.createFn
	s.mu.Unlock()
	if createFn == nil {
		return nil, errors.New("create function is not configured")
	}
	return createFn(req)
}

func (s *checkoutOrderFuncStub) GetOrder(_ context.Context, orderID, userID int64) (*dbent.PaymentOrder, error) {
	if s.order == nil || s.order.ID != orderID || s.order.UserID != userID {
		return nil, errors.New("order not found")
	}
	return s.order, nil
}

func (s *checkoutOrderFuncStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newCheckoutSessionTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.NewMiniRedis()
	if err := mr.Start(); err != nil {
		t.Skipf("miniredis unavailable in restricted environment: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func TestCheckoutSessionCreateAndPollBindsUserAndDevice(t *testing.T) {
	rdb := newCheckoutSessionTestRedis(t)
	stub := &checkoutOrderStub{
		created: &CreateOrderResponse{
			OrderID: 99, Amount: 10, PayAmount: 10.3, PaymentType: "alipay",
			Status: OrderStatusPending, PayURL: "https://pay.example.test/cashier/99",
			ClientSecret: "should-not-be-cached",
		},
		order: &dbent.PaymentOrder{ID: 99, UserID: 7, Status: OrderStatusPaid, PaymentType: "alipay", UpdatedAt: time.Now().UTC()},
	}
	svc := NewCheckoutSessionService(rdb, stub)
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	session, err := svc.Create(context.Background(), CreateCheckoutSessionInput{
		UserID:   7,
		DeviceID: "device-1",
		Request:  CreateOrderRequest{Amount: 10, PaymentType: "alipay", ReturnURL: "https://evil.example/redirect", IsMobile: true},
	})
	require.NoError(t, err)
	require.NotEmpty(t, session.SessionID)
	require.Equal(t, "/purchase?checkout_session_id="+session.SessionID, session.BrowserURL)
	require.Equal(t, OrderStatusPending, session.Status)
	// Creating a desktop session only reserves an opaque capability. The order
	// (and any provider response) is deferred until the browser activates it.
	require.Empty(t, stub.req.PaymentType)

	raw, err := rdb.Get(context.Background(), checkoutSessionKey(session.SessionID)).Result()
	require.NoError(t, err)
	require.NotContains(t, raw, "should-not-be-cached")

	reserved, err := svc.Get(context.Background(), 7, "device-1", session.SessionID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPending, reserved.Status)
	require.Zero(t, reserved.OrderID)

	activated, err := svc.Activate(context.Background(), 7, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(99), activated.OrderID)
	require.Equal(t, "hosted_redirect", stub.req.PaymentSource)
	require.False(t, stub.req.IsMobile)
	require.Equal(t, "https://ai.clol.site/payment/result", stub.req.ReturnURL)
	require.Empty(t, stub.req.SrcURL)
	require.Equal(t, "ai.clol.site", stub.req.SrcHost)

	polled, err := svc.Get(context.Background(), 7, "device-1", session.SessionID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPaid, polled.Status)
	require.Equal(t, int64(99), polled.OrderID)
	// The hosted URL is opened in a browser session for the same account; the
	// browser JWT has no device id but remains bound by user id.
	_, err = svc.Get(context.Background(), 7, "", session.SessionID)
	require.NoError(t, err)

	_, err = svc.Get(context.Background(), 7, "other-device", session.SessionID)
	require.ErrorIs(t, err, ErrCheckoutSessionForbidden)
	_, err = svc.Get(context.Background(), 8, "device-1", session.SessionID)
	require.ErrorIs(t, err, ErrCheckoutSessionNotFound)
	_, err = svc.Activate(context.Background(), 7, session.SessionID)
	require.ErrorIs(t, err, ErrCheckoutSessionActivated)
}

func TestCheckoutSessionIgnoresForgedBrowserHostForReturnURL(t *testing.T) {
	rdb := newCheckoutSessionTestRedis(t)
	stub := &checkoutOrderStub{created: &CreateOrderResponse{OrderID: 1, Status: OrderStatusPending}}
	svc := NewCheckoutSessionService(rdb, stub)
	session, err := svc.Create(context.Background(), CreateCheckoutSessionInput{
		UserID: 1, Request: CreateOrderRequest{Amount: 1, PaymentType: "alipay"},
	})
	require.NoError(t, err)

	_, err = svc.ActivateWithBrowserContext(context.Background(), 1, session.SessionID, CheckoutBrowserContext{
		Host: "evil.example.test", Scheme: "https", ClientIP: "203.0.113.10",
	})
	require.NoError(t, err)
	require.Equal(t, "ai.clol.site", stub.req.SrcHost)
	require.Equal(t, "https://ai.clol.site/payment/result", stub.req.ReturnURL)
}

func TestCheckoutSessionRejectsExpiredOrMalformedCapabilities(t *testing.T) {
	rdb := newCheckoutSessionTestRedis(t)
	stub := &checkoutOrderStub{created: &CreateOrderResponse{OrderID: 1, Status: OrderStatusPending}}
	svc := NewCheckoutSessionService(rdb, stub)
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	session, err := svc.Create(context.Background(), CreateCheckoutSessionInput{UserID: 1, Request: CreateOrderRequest{PaymentType: "alipay", Amount: 1}})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(session.StatusURL, "/api/v1/desktop/checkout-sessions/"))

	svc.now = func() time.Time { return now.Add(checkoutSessionTTL + time.Second) }
	_, err = svc.Get(context.Background(), 1, "", session.SessionID)
	require.ErrorIs(t, err, ErrCheckoutSessionNotFound)
	_, err = svc.Get(context.Background(), 1, "", "not a valid session id")
	require.ErrorIs(t, err, ErrCheckoutSessionNotFound)
}

func TestCheckoutSessionActivationSingleFlightAndDurableMarker(t *testing.T) {
	rdb := newCheckoutSessionTestRedis(t)
	providerStarted := make(chan struct{})
	providerRelease := make(chan struct{})
	var providerStartedOnce sync.Once
	stub := &checkoutOrderFuncStub{
		order: &dbent.PaymentOrder{ID: 42, UserID: 7, Status: OrderStatusPending, PaymentType: "alipay", UpdatedAt: time.Now().UTC()},
		createFn: func(CreateOrderRequest) (*CreateOrderResponse, error) {
			providerStartedOnce.Do(func() { close(providerStarted) })
			<-providerRelease
			return &CreateOrderResponse{OrderID: 42, Amount: 10, PayAmount: 10, PaymentType: "alipay", Status: OrderStatusPending}, nil
		},
	}
	svc := NewCheckoutSessionService(rdb, stub)
	session, err := svc.Create(context.Background(), CreateCheckoutSessionInput{
		UserID:  7,
		Request: CreateOrderRequest{Amount: 10, PaymentType: "alipay"},
	})
	require.NoError(t, err)

	firstResult := make(chan error, 1)
	go func() {
		_, activateErr := svc.Activate(context.Background(), 7, session.SessionID)
		firstResult <- activateErr
	}()
	select {
	case <-providerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("provider call did not start")
	}

	// The marker is written before entering the provider. A second request is
	// rejected while the first request owns the lock.
	raw, err := rdb.Get(context.Background(), checkoutSessionKey(session.SessionID)).Result()
	require.NoError(t, err)
	var activating checkoutSessionRecord
	require.NoError(t, json.Unmarshal([]byte(raw), &activating))
	require.Equal(t, checkoutSessionStatusActivating, activating.Status)
	_, err = svc.Activate(context.Background(), 7, session.SessionID)
	require.ErrorIs(t, err, ErrCheckoutSessionActivating)

	// Even if the short lock expires while a provider call is still running,
	// the durable marker prevents a second order. This is the race the old
	// load-before-lock implementation could not close.
	require.NoError(t, rdb.Del(context.Background(), checkoutActivationKey(session.SessionID)).Err())
	_, err = svc.Activate(context.Background(), 7, session.SessionID)
	require.ErrorIs(t, err, ErrCheckoutSessionActivating)
	require.Equal(t, 1, stub.callCount())

	close(providerRelease)
	select {
	case activateErr := <-firstResult:
		require.NoError(t, activateErr)
	case <-time.After(2 * time.Second):
		t.Fatal("activation did not finish")
	}
	require.Equal(t, 1, stub.callCount())

	polled, err := svc.Get(context.Background(), 7, "", session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(42), polled.OrderID)
	_, err = svc.Activate(context.Background(), 7, session.SessionID)
	require.ErrorIs(t, err, ErrCheckoutSessionActivated)
}

func TestCheckoutSessionProviderFailureLeavesActivationMarker(t *testing.T) {
	rdb := newCheckoutSessionTestRedis(t)
	stub := &checkoutOrderFuncStub{
		createFn: func(CreateOrderRequest) (*CreateOrderResponse, error) {
			return nil, errors.New("provider response uncertain")
		},
	}
	svc := NewCheckoutSessionService(rdb, stub)
	session, err := svc.Create(context.Background(), CreateCheckoutSessionInput{
		UserID:  11,
		Request: CreateOrderRequest{Amount: 3, PaymentType: "stripe"},
	})
	require.NoError(t, err)

	_, err = svc.Activate(context.Background(), 11, session.SessionID)
	require.EqualError(t, err, "provider response uncertain")
	require.Equal(t, 1, stub.callCount())

	// The provider may have created an order before returning an error. Do not
	// clear the marker and permit a duplicate charge on an immediate retry.
	_, err = svc.Activate(context.Background(), 11, session.SessionID)
	require.ErrorIs(t, err, ErrCheckoutSessionActivating)
	require.Equal(t, 1, stub.callCount())
	polled, err := svc.Get(context.Background(), 11, "", session.SessionID)
	require.NoError(t, err)
	require.Equal(t, checkoutSessionStatusActivating, polled.Status)
}

func TestCheckoutSessionOrderCreatedWhenFinalCheckpointFailsCannotRetry(t *testing.T) {
	rdb := newCheckoutSessionTestRedis(t)
	var sessionID string
	stub := &checkoutOrderFuncStub{
		createFn: func(CreateOrderRequest) (*CreateOrderResponse, error) {
			// Remove the expiry after the pre-provider marker has been written. The
			// atomic KEEPTTL checkpoint must reject this invariant violation instead
			// of silently turning the capability into a permanent key.
			returnValue := rdb.Persist(context.Background(), checkoutSessionKey(sessionID))
			if err := returnValue.Err(); err != nil {
				return nil, err
			}
			return &CreateOrderResponse{OrderID: 77, Amount: 4, PayAmount: 4, PaymentType: "alipay", Status: OrderStatusPending}, nil
		},
	}
	svc := NewCheckoutSessionService(rdb, stub)
	session, err := svc.Create(context.Background(), CreateCheckoutSessionInput{
		UserID:  12,
		Request: CreateOrderRequest{Amount: 4, PaymentType: "alipay"},
	})
	require.NoError(t, err)
	sessionID = session.SessionID

	order, err := svc.Activate(context.Background(), 12, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(77), order.OrderID)
	require.Equal(t, 1, stub.callCount())

	// The provider order was returned to the browser, but Redis could not save
	// its id. The activating marker remains and makes retries non-creating.
	_, err = svc.Activate(context.Background(), 12, session.SessionID)
	require.ErrorIs(t, err, ErrCheckoutSessionActivating)
	require.Equal(t, 1, stub.callCount())

	raw, err := rdb.Get(context.Background(), checkoutSessionKey(session.SessionID)).Result()
	require.NoError(t, err)
	var record checkoutSessionRecord
	require.NoError(t, json.Unmarshal([]byte(raw), &record))
	require.Equal(t, checkoutSessionStatusActivating, record.Status)
	require.Zero(t, record.OrderID)
}

func TestCheckoutSessionPersistKeepsTTLAndRejectsUnboundedKeys(t *testing.T) {
	rdb := newCheckoutSessionTestRedis(t)
	stub := &checkoutOrderStub{created: &CreateOrderResponse{OrderID: 1, Status: OrderStatusPending}}
	svc := NewCheckoutSessionService(rdb, stub)
	session, err := svc.Create(context.Background(), CreateCheckoutSessionInput{
		UserID:  13,
		Request: CreateOrderRequest{Amount: 2, PaymentType: "alipay"},
	})
	require.NoError(t, err)
	key := checkoutSessionKey(session.SessionID)
	require.NoError(t, rdb.Expire(context.Background(), key, 2*time.Minute).Err())
	before, err := rdb.PTTL(context.Background(), key).Result()
	require.NoError(t, err)

	record, err := svc.loadRecord(context.Background(), 13, "", session.SessionID)
	require.NoError(t, err)
	record.Status = "checkpointed"
	require.NoError(t, svc.persistRecord(context.Background(), record))
	after, err := rdb.PTTL(context.Background(), key).Result()
	require.NoError(t, err)
	require.Greater(t, after, time.Duration(0))
	require.LessOrEqual(t, after, before)
	require.Less(t, before-after, time.Second)

	// A key without an expiry must never be rewritten by a session checkpoint.
	require.NoError(t, rdb.Persist(context.Background(), key).Err())
	require.ErrorIs(t, svc.persistRecord(context.Background(), record), ErrServiceUnavailable)
	require.NoError(t, rdb.Del(context.Background(), key).Err())
	require.ErrorIs(t, svc.persistRecord(context.Background(), record), ErrCheckoutSessionNotFound)
}

func TestCheckoutSessionActivationLockCompareAndDelete(t *testing.T) {
	rdb := newCheckoutSessionTestRedis(t)
	key := checkoutActivationKey("lock-test")
	require.NoError(t, rdb.Set(context.Background(), key, "new-owner", checkoutActivationLockTTL).Err())
	require.NoError(t, releaseCheckoutActivationLock(context.Background(), rdb, key, "old-owner"))
	value, err := rdb.Get(context.Background(), key).Result()
	require.NoError(t, err)
	require.Equal(t, "new-owner", value)
	require.NoError(t, releaseCheckoutActivationLock(context.Background(), rdb, key, "new-owner"))
	_, err = rdb.Get(context.Background(), key).Result()
	require.ErrorIs(t, err, redis.Nil)
}
