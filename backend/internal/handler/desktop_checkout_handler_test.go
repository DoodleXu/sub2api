//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type desktopCheckoutHandlerOrderStub struct {
	created *service.CreateOrderResponse
	order   *dbent.PaymentOrder
}

func (s *desktopCheckoutHandlerOrderStub) CreateOrder(context.Context, service.CreateOrderRequest) (*service.CreateOrderResponse, error) {
	return s.created, nil
}

func (s *desktopCheckoutHandlerOrderStub) GetOrder(_ context.Context, orderID, userID int64) (*dbent.PaymentOrder, error) {
	if s.order == nil || s.order.ID != orderID || s.order.UserID != userID {
		return nil, service.ErrCheckoutSessionNotFound
	}
	return s.order, nil
}

func TestDesktopCheckoutHandlerDoesNotExposePaymentCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.NewMiniRedis()
	if err := mr.Start(); err != nil {
		t.Skipf("miniredis unavailable in restricted environment: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	stub := &desktopCheckoutHandlerOrderStub{created: &service.CreateOrderResponse{
		OrderID: 10, Status: service.OrderStatusPending, PaymentType: "stripe",
		ClientSecret: "pi_secret_should_not_escape", ResumeToken: "resume_should_not_escape",
		PayURL: "https://pay.example.test/secret", QRCode: "qr_should_not_escape",
	}}
	h := NewDesktopCheckoutHandler(service.NewCheckoutSessionService(rdb, stub))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/desktop/checkout-sessions", strings.NewReader(`{"amount":10,"payment_type":"stripe"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
	h.Create(c)

	require.Equal(t, http.StatusCreated, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "pi_secret_should_not_escape")
	require.NotContains(t, recorder.Body.String(), "resume_should_not_escape")
	require.NotContains(t, recorder.Body.String(), "pay.example.test")
	require.NotContains(t, recorder.Body.String(), "qr_should_not_escape")
	var envelope struct {
		Data struct {
			SessionID  string `json:"session_id"`
			BrowserURL string `json:"browser_url"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.NotEmpty(t, envelope.Data.SessionID)
	require.Equal(t, "/purchase?checkout_session_id="+envelope.Data.SessionID, envelope.Data.BrowserURL)

	// The browser activation endpoint is the only place where provider details
	// are returned. The desktop create response above remains metadata-only.
	recorderActivate := httptest.NewRecorder()
	cActivate, _ := gin.CreateTestContext(recorderActivate)
	cActivate.Request = httptest.NewRequest(http.MethodPost, "/api/v1/desktop/checkout-sessions/"+envelope.Data.SessionID+"/activate", nil)
	cActivate.Params = gin.Params{{Key: "session_id", Value: envelope.Data.SessionID}}
	cActivate.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
	h.Activate(cActivate)
	require.Equal(t, http.StatusOK, recorderActivate.Code)
	require.Contains(t, recorderActivate.Body.String(), "pi_secret_should_not_escape")
	require.Contains(t, recorderActivate.Body.String(), "resume_should_not_escape")

	// Ensure the handler accepts a browser session for the same user when the
	// original session was created by a device.
	stub.order = &dbent.PaymentOrder{ID: 10, UserID: 7, Status: service.OrderStatusPaid, UpdatedAt: time.Now().UTC()}
	recorder2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(recorder2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/api/v1/desktop/checkout-sessions/"+envelope.Data.SessionID, nil)
	c2.Params = gin.Params{{Key: "session_id", Value: envelope.Data.SessionID}}
	c2.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
	h.Get(c2)
	require.Equal(t, http.StatusOK, recorder2.Code)
}
