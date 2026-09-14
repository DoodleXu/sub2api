package provider

import (
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

const hashPayMaxBody = 1 << 20

type HashPay struct {
	instanceID string
	config     map[string]string
	privateKey *rsa.PrivateKey
	httpClient *http.Client
}

func NewHashPay(instanceID string, config map[string]string) (*HashPay, error) {
	for _, k := range []string{"merchantId", "privateKey", "apiBase"} {
		if strings.TrimSpace(config[k]) == "" {
			return nil, fmt.Errorf("hashpay config missing required key: %s", k)
		}
	}
	key, err := parseRSAPrivateKey(config["privateKey"])
	if err != nil {
		return nil, fmt.Errorf("hashpay privateKey: %w", err)
	}
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(config["apiBase"]), "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, fmt.Errorf("hashpay apiBase must be an HTTPS URL")
	}
	cfg := cloneStringMap(config)
	cfg["apiBase"] = strings.TrimRight(base.String(), "/")
	if cfg["currency"] == "" {
		cfg["currency"] = "USD"
	}
	return &HashPay{instanceID: instanceID, config: cfg, privateKey: key, httpClient: &http.Client{Timeout: 15 * time.Second}}, nil
}
func (h *HashPay) Name() string        { return "HashPay" }
func (h *HashPay) ProviderKey() string { return payment.TypeHashPay }
func (h *HashPay) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeHashPay}
}
func (h *HashPay) MerchantIdentityMetadata() map[string]string {
	return map[string]string{"merchant_id": h.config["merchantId"], "currency": h.config["currency"]}
}

func (h *HashPay) request(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	digest := method + "\n" + path + "\n" + ts + "\n" + string(body)
	hash := sha256.Sum256([]byte(digest))
	sig, err := rsa.SignPKCS1v15(rand.Reader, h.privateKey, crypto.SHA256, hash[:])
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, h.config["apiBase"]+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Merchant-Id", h.config["merchantId"])
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Signature", base64.StdEncoding.EncodeToString(sig))
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, hashPayMaxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("hashpay HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return out, nil
}
func (h *HashPay) CreatePayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	amount, err := strconv.ParseFloat(strings.TrimSpace(req.Amount), 64)
	if err != nil || amount <= 0 {
		return nil, fmt.Errorf("hashpay create payment: invalid amount %s", req.Amount)
	}
	p := map[string]any{"merchantNo": req.OrderID, "amount": amount, "currency": h.config["currency"], "description": req.Subject, "return_url": req.ReturnURL, "callback": req.NotifyURL}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	raw, err := h.request(ctx, http.MethodPost, "/api/merchant/new", b)
	if err != nil {
		return nil, err
	}
	var r struct {
		CheckoutURL string `json:"checkoutUrl"`
		Order       struct {
			ID string `json:"id"`
		} `json:"order"`
	}
	if err = json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.CheckoutURL) == "" || strings.TrimSpace(r.Order.ID) == "" {
		return nil, fmt.Errorf("hashpay create response missing checkoutUrl or order.id")
	}
	return &payment.CreatePaymentResponse{TradeNo: r.Order.ID, PayURL: r.CheckoutURL, Currency: h.config["currency"]}, nil
}
func (h *HashPay) QueryOrder(ctx context.Context, tradeNo string) (*payment.QueryOrderResponse, error) {
	raw, err := h.request(ctx, http.MethodGet, "/api/order/"+url.PathEscape(tradeNo), nil)
	if err != nil {
		return nil, err
	}
	var r struct {
		ID     string  `json:"id"`
		Status string  `json:"status"`
		Amount float64 `json:"amount"`
	}
	if err = json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.ID) == "" || !strings.EqualFold(r.ID, tradeNo) || r.Amount <= 0 || strings.TrimSpace(r.Status) == "" {
		return nil, fmt.Errorf("hashpay query response is incomplete or mismatched")
	}
	return &payment.QueryOrderResponse{TradeNo: r.ID, Status: strings.ToLower(r.Status), Amount: r.Amount}, nil
}
func (h *HashPay) VerifyNotification(_ context.Context, raw string, headers map[string]string) (*payment.PaymentNotification, error) {
	merchant := strings.TrimSpace(headers["x-hashpay-merchant"])
	if merchant == "" || merchant != strings.TrimSpace(h.config["merchantId"]) {
		return nil, fmt.Errorf("hashpay callback merchant mismatch")
	}
	headerTS, err := strconv.ParseInt(strings.TrimSpace(headers["x-hashpay-timestamp"]), 10, 64)
	if err != nil || headerTS <= 0 {
		return nil, fmt.Errorf("hashpay callback timestamp is required")
	}
	var e struct {
		Alg  string `json:"alg"`
		Key  string `json:"key"`
		IV   string `json:"iv"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return nil, err
	}
	if e.Alg != "RSA-OAEP-256+A256GCM" {
		return nil, fmt.Errorf("hashpay unsupported encryption")
	}
	kb, err := base64.StdEncoding.DecodeString(e.Key)
	if err != nil {
		return nil, err
	}
	aesKey, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, h.privateKey, kb, nil)
	if err != nil {
		return nil, err
	}
	if len(aesKey) != 32 {
		return nil, fmt.Errorf("hashpay AES key must be 32 bytes")
	}
	iv, err := base64.StdEncoding.DecodeString(e.IV)
	if err != nil {
		return nil, err
	}
	ct, err := base64.StdEncoding.DecodeString(e.Data)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := g.Open(nil, iv, ct, nil)
	if err != nil {
		return nil, err
	}
	var msg struct {
		Timestamp int64 `json:"timestamp"`
		Payload   struct {
			OrderID    string  `json:"orderId"`
			MerchantNo string  `json:"merchantNo"`
			Status     string  `json:"status"`
			Amount     float64 `json:"amount"`
			Currency   string  `json:"currency"`
		} `json:"payload"`
	}
	if err = json.Unmarshal(plain, &msg); err != nil {
		return nil, err
	}
	ts := msg.Timestamp
	if ts == 0 || ts != headerTS {
		return nil, fmt.Errorf("hashpay callback timestamp mismatch")
	}
	age := time.Since(time.Unix(ts, 0))
	if ts == 0 || age > 5*time.Minute || age < -5*time.Minute {
		return nil, fmt.Errorf("hashpay callback timestamp outside window")
	}
	status := strings.ToLower(msg.Payload.Status)
	if status != "paid" && status != "success" {
		return nil, nil
	}
	return &payment.PaymentNotification{TradeNo: msg.Payload.OrderID, OrderID: msg.Payload.MerchantNo, Amount: msg.Payload.Amount, Status: payment.NotificationStatusSuccess, RawData: raw, Metadata: map[string]string{"currency": msg.Payload.Currency}}, nil
}
func (h *HashPay) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	return nil, fmt.Errorf("hashpay refunds are not supported")
}
func parseRSAPrivateKey(raw string) (*rsa.PrivateKey, error) {
	b, _ := pem.Decode([]byte(raw))
	if b == nil {
		return nil, fmt.Errorf("invalid PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	r, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not RSA private key")
	}
	return r, nil
}
