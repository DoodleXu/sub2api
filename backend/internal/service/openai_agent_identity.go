package service

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

const (
	OpenAIAuthModeAgentIdentity          = "agentIdentity"
	agentIdentityAuthAPIBaseURL          = "https://auth.openai.com/api/accounts"
	agentIdentityTaskRegistrationTimeout = 30 * time.Second
)

var openAIAgentIdentityAuthAPIBaseURL = agentIdentityAuthAPIBaseURL

var agentIdentityTaskLocks sync.Map // map[int64]*sync.Mutex

type agentIdentityWSConnectionInvalidator interface {
	InvalidateAgentIdentityWSConnections(accountID int64)
}

type agentIdentityKey struct {
	runtimeID  string
	privateKey ed25519.PrivateKey
	taskID     string
}

type agentIdentityTaskRegistrationResponse struct {
	TaskID               string `json:"task_id"`
	TaskIDCamel          string `json:"taskId"`
	EncryptedTaskID      string `json:"encrypted_task_id"`
	EncryptedTaskIDCamel string `json:"encryptedTaskId"`
}

func (a *Account) IsOpenAIAgentIdentity() bool {
	if a == nil || !a.IsOpenAIOAuth() {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(a.GetCredential(openAIAuthModeCredentialKey)), OpenAIAuthModeAgentIdentity)
}

func agentIdentityPrivateKey(account *Account) (ed25519.PrivateKey, error) {
	if account == nil {
		return nil, errors.New("agent identity account is nil")
	}
	raw := strings.TrimSpace(account.GetCredential("agent_private_key"))
	if raw == "" {
		return nil, errors.New("agent identity private key is missing")
	}
	der, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("agent identity private key is not valid base64")
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, errors.New("agent identity private key is not valid PKCS#8")
	}
	privateKey, ok := key.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("agent identity private key is not Ed25519")
	}
	return privateKey, nil
}

// ValidateOpenAIAgentIdentityPrivateKey validates the stored PKCS#8 Ed25519
// form without returning or logging the key material.
func ValidateOpenAIAgentIdentityPrivateKey(encoded string) error {
	account := &Account{Credentials: map[string]any{"agent_private_key": encoded}}
	_, err := agentIdentityPrivateKey(account)
	return err
}

func agentIdentityKeyFromAccount(account *Account) (agentIdentityKey, error) {
	privateKey, err := agentIdentityPrivateKey(account)
	if err != nil {
		return agentIdentityKey{}, err
	}
	runtimeID := strings.TrimSpace(account.GetCredential("agent_runtime_id"))
	if runtimeID == "" {
		return agentIdentityKey{}, errors.New("agent identity runtime id is missing")
	}
	return agentIdentityKey{
		runtimeID:  runtimeID,
		privateKey: privateKey,
		taskID:     strings.TrimSpace(account.GetCredential("task_id")),
	}, nil
}

func buildAgentAssertion(key agentIdentityKey, now time.Time) (string, error) {
	if key.runtimeID == "" || key.taskID == "" {
		return "", errors.New("agent identity runtime or task id is missing")
	}
	timestamp := now.UTC().Format(time.RFC3339)
	payload := []byte(key.runtimeID + ":" + key.taskID + ":" + timestamp)
	signature, err := key.privateKey.Sign(nil, payload, crypto.Hash(0))
	if err != nil {
		return "", errors.New("failed to sign agent assertion")
	}
	envelope := map[string]string{
		"agent_runtime_id": key.runtimeID,
		"task_id":          key.taskID,
		"timestamp":        timestamp,
		"signature":        base64.StdEncoding.EncodeToString(signature),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", errors.New("failed to serialize agent assertion")
	}
	return "AgentAssertion " + base64.RawURLEncoding.EncodeToString(encoded), nil
}

func agentIdentityTaskIDFromAuthorization(value string) string {
	const prefix = "AgentAssertion "
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, prefix) {
		return ""
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(value, prefix)))
	if err != nil {
		return ""
	}
	var envelope struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(decoded, &envelope); err != nil {
		return ""
	}
	return strings.TrimSpace(envelope.TaskID)
}

func signAgentTaskRegistration(key agentIdentityKey, timestamp time.Time) (string, string, error) {
	if key.runtimeID == "" {
		return "", "", errors.New("agent identity runtime id is missing")
	}
	formatted := timestamp.UTC().Format(time.RFC3339)
	signature, err := key.privateKey.Sign(nil, []byte(key.runtimeID+":"+formatted), crypto.Hash(0))
	if err != nil {
		return "", "", errors.New("failed to sign agent task registration")
	}
	return formatted, base64.StdEncoding.EncodeToString(signature), nil
}

func decryptAgentTaskID(key agentIdentityKey, encoded string) (string, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", errors.New("encrypted agent task id is not valid base64")
	}
	seed := key.privateKey.Seed()
	digest := sha512.Sum512(seed)
	var curvePrivate [32]byte
	copy(curvePrivate[:], digest[:32])
	curvePrivate[0] &= 248
	curvePrivate[31] &= 127
	curvePrivate[31] |= 64
	curvePublicBytes, err := curve25519.X25519(curvePrivate[:], curve25519.Basepoint)
	if err != nil {
		return "", errors.New("failed to derive agent identity decryption key")
	}
	var curvePublic [32]byte
	copy(curvePublic[:], curvePublicBytes)
	plaintext, ok := box.OpenAnonymous(nil, ciphertext, &curvePublic, &curvePrivate)
	if !ok {
		return "", errors.New("failed to decrypt encrypted agent task id")
	}
	taskID := strings.TrimSpace(string(plaintext))
	if taskID == "" {
		return "", errors.New("decrypted agent task id is empty")
	}
	return taskID, nil
}

func registerAgentIdentityTask(ctx context.Context, account *Account) (string, error) {
	key, err := agentIdentityKeyFromAccount(account)
	if err != nil {
		return "", err
	}
	timestamp, signature, err := signAgentTaskRegistration(key, time.Now())
	if err != nil {
		return "", err
	}
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	client, err := httpclient.GetClient(httpclient.Options{
		ProxyURL:              proxyURL,
		Timeout:               agentIdentityTaskRegistrationTimeout,
		ResponseHeaderTimeout: 15 * time.Second,
	})
	if err != nil {
		return "", errors.New("invalid proxy configuration for agent task registration")
	}
	body, err := json.Marshal(map[string]string{
		"timestamp": timestamp,
		"signature": signature,
	})
	if err != nil {
		return "", errors.New("failed to serialize agent task registration")
	}
	url := strings.TrimRight(strings.TrimSpace(openAIAgentIdentityAuthAPIBaseURL), "/") + "/v1/agent/" + key.runtimeID + "/task/register"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return "", errors.New("failed to build agent task registration request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("agent task registration request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("agent task registration returned status %d", resp.StatusCode)
	}
	var result agentIdentityTaskRegistrationResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&result); err != nil {
		return "", errors.New("agent task registration response is invalid")
	}
	if taskID := strings.TrimSpace(result.TaskID); taskID != "" {
		return taskID, nil
	}
	if taskID := strings.TrimSpace(result.TaskIDCamel); taskID != "" {
		return taskID, nil
	}
	encrypted := strings.TrimSpace(result.EncryptedTaskID)
	if encrypted == "" {
		encrypted = strings.TrimSpace(result.EncryptedTaskIDCamel)
	}
	if encrypted == "" {
		return "", errors.New("agent task registration response omitted task id")
	}
	return decryptAgentTaskID(key, encrypted)
}

func ensureAgentIdentityTaskForAccount(ctx context.Context, repo AccountRepository, wsInvalidator agentIdentityWSConnectionInvalidator, taskMu *sync.Mutex, account *Account, expectedTaskID string) error {
	if account == nil {
		return nil
	}
	credAccount := account
	if account.IsShadow() {
		resolved, err := resolveCredentialAccount(ctx, repo, account)
		if err != nil {
			return err
		}
		credAccount = resolved
	}
	if credAccount == nil || !credAccount.IsOpenAIAgentIdentity() {
		return nil
	}
	currentTaskID := strings.TrimSpace(credAccount.GetCredential("task_id"))
	if currentTaskID != "" && (expectedTaskID == "" || currentTaskID != expectedTaskID) {
		return nil
	}
	if taskMu == nil {
		return errors.New("agent identity task lock is unavailable")
	}
	sharedTaskMu := taskMu
	if credAccount.ID > 0 {
		candidate := &sync.Mutex{}
		actual, _ := agentIdentityTaskLocks.LoadOrStore(credAccount.ID, candidate)
		loadedTaskMu, ok := actual.(*sync.Mutex)
		if !ok {
			return errors.New("agent identity task lock has invalid type")
		}
		sharedTaskMu = loadedTaskMu
	}
	sharedTaskMu.Lock()
	defer sharedTaskMu.Unlock()
	// Re-read inside the shared lock. Different request paths often receive
	// independent repository snapshots; checking only the caller's snapshot
	// would allow sequential duplicate registrations after the first writer
	// has already persisted a new task.
	if repo != nil && credAccount.ID > 0 {
		refreshed, refreshErr := repo.GetByID(ctx, credAccount.ID)
		if refreshErr != nil {
			return fmt.Errorf("refresh agent identity account %d: %w", credAccount.ID, refreshErr)
		}
		if refreshed == nil {
			return fmt.Errorf("refresh agent identity account %d: account not found", credAccount.ID)
		}
		if refreshed.IsShadow() {
			resolved, resolveErr := resolveCredentialAccount(ctx, repo, refreshed)
			if resolveErr != nil {
				return resolveErr
			}
			if resolved == nil {
				return errors.New("agent identity credential account is unavailable")
			}
			refreshed = resolved
		}
		if !refreshed.IsOpenAIAgentIdentity() {
			return errors.New("agent identity account authentication mode changed")
		}
		credAccount = refreshed
		if !account.IsShadow() {
			account.Credentials = shallowCopyMap(credAccount.Credentials)
		}
	}
	currentTaskID = strings.TrimSpace(credAccount.GetCredential("task_id"))
	if currentTaskID != "" && (expectedTaskID == "" || currentTaskID != expectedTaskID) {
		return nil
	}
	newTaskID, err := registerAgentIdentityTask(ctx, credAccount)
	if err != nil {
		return err
	}
	credentials := make(map[string]any, len(credAccount.Credentials)+1)
	for key, value := range credAccount.Credentials {
		credentials[key] = value
	}
	credentials["task_id"] = newTaskID
	if err := persistAccountCredentials(ctx, repo, credAccount, credentials); err != nil {
		return err
	}
	if !account.IsShadow() && account != credAccount {
		account.Credentials = shallowCopyMap(credAccount.Credentials)
	}
	if wsInvalidator != nil {
		wsInvalidator.InvalidateAgentIdentityWSConnections(credAccount.ID)
	}
	return nil
}

func (s *OpenAIGatewayService) ensureAgentIdentityTask(ctx context.Context, account *Account, expectedTaskID string) error {
	if s == nil {
		return errors.New("openai gateway service is nil")
	}
	return ensureAgentIdentityTaskForAccount(ctx, s.accountRepo, s, &s.agentIdentityTaskMu, account, expectedTaskID)
}

func isAgentIdentityTaskInvalidHTTPResponse(statusCode int, body []byte) bool {
	if statusCode != http.StatusUnauthorized {
		return false
	}
	lower := strings.ToLower(string(body))
	compact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(lower)
	for _, marker := range []string{
		`"code":"invalid_task_id"`,
		`"code":"task_not_found"`,
		`"code":"task_expired"`,
		`"error":"invalid_task_id"`,
	} {
		if strings.Contains(compact, marker) {
			return true
		}
	}
	for _, marker := range []string{
		"invalid task_id",
		"invalid task id",
		"task_id is invalid",
		"task id is invalid",
		"task not found",
		"task expired",
		"unknown task_id",
		"unknown task id",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

type agentIdentityTaskRecoveryContextKey struct{}

func markAgentIdentityTaskRecoveryTried(ctx context.Context) context.Context {
	return context.WithValue(ctx, agentIdentityTaskRecoveryContextKey{}, true)
}

func agentIdentityTaskRecoveryWasTried(ctx context.Context) bool {
	tried, _ := ctx.Value(agentIdentityTaskRecoveryContextKey{}).(bool)
	return tried
}

func isAgentIdentityTaskInvalidWSDialError(err *openAIWSDialError) bool {
	return err != nil && isAgentIdentityTaskInvalidHTTPResponse(err.StatusCode, err.ResponseBody)
}

func (s *OpenAIGatewayService) buildOpenAIAuthenticationHeaders(ctx context.Context, account *Account, token string) (http.Header, error) {
	if account == nil {
		return nil, errors.New("account is nil")
	}
	credAccount := account
	if account.IsShadow() {
		resolved, err := resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return nil, err
		}
		credAccount = resolved
	}
	headers := make(http.Header)
	if credAccount != nil && credAccount.IsOpenAIAgentIdentity() {
		agentHeaders, err := buildAgentIdentityAuthenticationHeaders(ctx, s.accountRepo, s, &s.agentIdentityTaskMu, credAccount)
		if err != nil {
			return nil, err
		}
		return agentHeaders, nil
	}
	headers.Set("Authorization", "Bearer "+token)
	return headers, nil
}

func buildAgentIdentityAuthenticationHeaders(ctx context.Context, repo AccountRepository, wsInvalidator agentIdentityWSConnectionInvalidator, taskMu *sync.Mutex, account *Account) (http.Header, error) {
	if account == nil || !account.IsOpenAIAgentIdentity() {
		return nil, errors.New("agent identity account is required")
	}
	if err := ensureAgentIdentityTaskForAccount(ctx, repo, wsInvalidator, taskMu, account, ""); err != nil {
		return nil, err
	}
	key, err := agentIdentityKeyFromAccount(account)
	if err != nil {
		return nil, err
	}
	assertion, err := buildAgentAssertion(key, time.Now())
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("Authorization", assertion)
	return headers, nil
}

func (s *OpenAIGatewayService) refreshOpenAIAgentIdentityHeaders(ctx context.Context, account *Account, headers http.Header) (http.Header, error) {
	if account == nil {
		return cloneHeader(headers), nil
	}
	credAccount := account
	if account.IsShadow() {
		resolved, err := resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return nil, err
		}
		credAccount = resolved
	}
	if !credAccount.IsOpenAIAgentIdentity() {
		return cloneHeader(headers), nil
	}
	refreshed := cloneHeader(headers)
	if refreshed == nil {
		refreshed = make(http.Header)
	}
	authHeaders, err := buildAgentIdentityAuthenticationHeaders(ctx, s.accountRepo, s, &s.agentIdentityTaskMu, credAccount)
	if err != nil {
		return nil, err
	}
	refreshed.Set("Authorization", authHeaders.Get("Authorization"))
	return refreshed, nil
}

func (s *OpenAIGatewayService) recoverAgentIdentityTask(ctx context.Context, account *Account, expectedTaskID string) error {
	return s.ensureAgentIdentityTask(ctx, account, expectedTaskID)
}

// redactAgentIdentitySensitiveBody removes credential values before an
// upstream error can reach logs, ops events, or returned error text. Agent
// Identity responses should not echo these values, but keeping this boundary
// defensive prevents accidental disclosure if an upstream error does.
func redactAgentIdentitySensitiveBodyForAccount(ctx context.Context, repo AccountRepository, account *Account, body []byte) []byte {
	if account == nil || len(body) == 0 {
		return body
	}
	credAccount := account
	if account != nil && account.IsShadow() {
		if resolved, err := resolveCredentialAccount(ctx, repo, account); err == nil && resolved != nil {
			credAccount = resolved
		}
	}
	if credAccount == nil || !credAccount.IsOpenAIAgentIdentity() {
		return body
	}
	return redactAgentIdentitySensitiveBodyWithValues(body, agentIdentitySensitiveValues(credAccount))
}

func agentIdentitySensitiveValues(account *Account) []string {
	if account == nil || !account.IsOpenAIAgentIdentity() {
		return nil
	}
	values := make([]string, 0, 12)
	for _, key := range []string{
		"agent_private_key",
		"agent_runtime_id",
		"task_id",
		"access_token",
		"refresh_token",
		"id_token",
		"api_key",
		"session_key",
		"cookie",
	} {
		if value := strings.TrimSpace(account.GetCredential(key)); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func redactAgentIdentitySensitiveBodyWithValues(body []byte, values []string) []byte {
	redacted := string(body)
	for _, value := range values {
		redacted = strings.ReplaceAll(redacted, value, "[redacted]")
	}
	const assertionPrefix = "AgentAssertion "
	for offset := 0; offset < len(redacted); {
		relativeStart := strings.Index(redacted[offset:], assertionPrefix)
		if relativeStart < 0 {
			break
		}
		start := offset + relativeStart
		valueStart := start + len(assertionPrefix)
		end := valueStart
		for end < len(redacted) && !strings.ContainsRune(" \t\r\n\"',}", rune(redacted[end])) {
			end++
		}
		redacted = redacted[:valueStart] + "[redacted]" + redacted[end:]
		offset = valueStart + len("[redacted]")
	}
	return []byte(redacted)
}

func (s *OpenAIGatewayService) redactAgentIdentitySensitiveBody(ctx context.Context, account *Account, body []byte) []byte {
	redactor, err := s.agentIdentitySensitiveBodyRedactor(ctx, account)
	if err != nil {
		return agentIdentityFailClosedBody(body)
	}
	return redactor(body)
}

type agentIdentityBodyRedactor func([]byte) []byte

type agentIdentityRedactionState struct {
	mu          sync.RWMutex
	base        agentIdentityBodyRedactor
	extraValues map[string]struct{}
}

func newAgentIdentityRedactionState(base agentIdentityBodyRedactor) *agentIdentityRedactionState {
	if base == nil {
		base = func(body []byte) []byte { return body }
	}
	return &agentIdentityRedactionState{
		base:        base,
		extraValues: make(map[string]struct{}),
	}
}

func (s *agentIdentityRedactionState) add(value string) {
	if s == nil {
		return
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	s.mu.Lock()
	s.extraValues[value] = struct{}{}
	s.mu.Unlock()
}

func (s *agentIdentityRedactionState) redact(body []byte) []byte {
	if s == nil {
		return body
	}
	redacted := s.base(body)
	s.mu.RLock()
	values := make([]string, 0, len(s.extraValues))
	for value := range s.extraValues {
		values = append(values, value)
	}
	s.mu.RUnlock()
	for _, value := range values {
		redacted = []byte(strings.ReplaceAll(string(redacted), value, "[redacted]"))
	}
	return redacted
}

type agentIdentityRequestMetadata struct {
	credentialAccount *Account
	isAgentIdentity   bool
	taskIDUsed        string
	redactor          agentIdentityBodyRedactor
	redactionState    *agentIdentityRedactionState
}

func (m *agentIdentityRequestMetadata) bindTaskID(taskID string) {
	if m == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	m.taskIDUsed = taskID
	m.redactionState.add(taskID)
}

func (m *agentIdentityRequestMetadata) bindAuthorization(value string) {
	if m == nil {
		return
	}
	value = strings.TrimSpace(value)
	if value != "" {
		// Bind the complete on-wire value and the bare assertion token. The
		// latter protects against upstreams echoing the token without its scheme.
		m.redactionState.add(value)
		const prefix = "AgentAssertion "
		if strings.HasPrefix(value, prefix) {
			m.redactionState.add(strings.TrimSpace(strings.TrimPrefix(value, prefix)))
		}
	}
	m.bindTaskID(agentIdentityTaskIDFromAuthorization(value))
}

func (s *OpenAIGatewayService) agentIdentityRequestMetadata(ctx context.Context, account *Account) (agentIdentityRequestMetadata, error) {
	credentialAccount := account
	if account != nil && account.IsShadow() {
		resolved, err := resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return agentIdentityRequestMetadata{}, err
		}
		if resolved == nil {
			return agentIdentityRequestMetadata{}, errors.New("agent identity credential account is unavailable")
		}
		credentialAccount = resolved
	}
	metadata := agentIdentityRequestMetadata{
		credentialAccount: credentialAccount,
	}
	metadata.redactionState = newAgentIdentityRedactionState(func(body []byte) []byte { return body })
	metadata.redactor = metadata.redactionState.redact
	if credentialAccount == nil || !credentialAccount.IsOpenAIAgentIdentity() {
		return metadata, nil
	}
	metadata.isAgentIdentity = true
	// Capture sensitive values once. Credential refresh replaces the account's
	// map while a request may still be streaming; frame redaction must use an
	// immutable request snapshot instead of reading that map on every frame.
	sensitiveValues := agentIdentitySensitiveValues(credentialAccount)
	metadata.redactionState = newAgentIdentityRedactionState(func(body []byte) []byte {
		return redactAgentIdentitySensitiveBodyWithValues(body, sensitiveValues)
	})
	metadata.redactor = metadata.redactionState.redact
	metadata.bindTaskID(credentialAccount.GetCredential("task_id"))
	return metadata, nil
}

type agentIdentityRequestRedactorContextKey struct{}

func withAgentIdentityRequestRedactor(ctx context.Context, redactor agentIdentityBodyRedactor) context.Context {
	if ctx == nil || redactor == nil {
		return ctx
	}
	return context.WithValue(ctx, agentIdentityRequestRedactorContextKey{}, redactor)
}

func agentIdentityRequestRedactor(ctx context.Context) agentIdentityBodyRedactor {
	if ctx == nil {
		return nil
	}
	redactor, _ := ctx.Value(agentIdentityRequestRedactorContextKey{}).(agentIdentityBodyRedactor)
	return redactor
}

func agentIdentityFailClosedBody(body []byte) []byte {
	var envelope struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(body, &envelope)
	eventType := strings.TrimSpace(envelope.Type)
	if eventType == "" {
		return []byte(`{"error":{"type":"upstream_error","message":"[redacted]"}}`)
	}
	safe := map[string]any{
		"type": eventType,
		"error": map[string]any{
			"type":    "upstream_error",
			"message": "[redacted]",
		},
	}
	if eventType == "response.failed" || eventType == "response.incomplete" {
		safe["response"] = map[string]any{
			"status": strings.TrimPrefix(eventType, "response."),
			"error": map[string]any{
				"type":    "upstream_error",
				"message": "[redacted]",
			},
		}
	}
	encoded, err := json.Marshal(safe)
	if err != nil {
		return []byte(`{"type":"error","error":{"type":"upstream_error","message":"[redacted]"}}`)
	}
	return encoded
}

// agentIdentitySensitiveBodyRedactor resolves a shadow's credential account
// once per upstream request. Streaming paths must not resolve the parent for
// every SSE/WS frame, which otherwise turns a long response into an unbounded
// sequence of repository reads. Resolution errors are returned so callers can
// fail closed instead of forwarding an error that may contain credentials.
func (s *OpenAIGatewayService) agentIdentitySensitiveBodyRedactor(ctx context.Context, account *Account) (agentIdentityBodyRedactor, error) {
	if redactor := agentIdentityRequestRedactor(ctx); redactor != nil {
		return redactor, nil
	}
	metadata, err := s.agentIdentityRequestMetadata(ctx, account)
	if err != nil {
		return nil, err
	}
	return metadata.redactor, nil
}

func redactAgentIdentitySensitiveText(redactor agentIdentityBodyRedactor, value string) string {
	if redactor == nil || value == "" {
		return value
	}
	return string(redactor([]byte(value)))
}

func redactAgentIdentitySensitiveError(redactor agentIdentityBodyRedactor, err error) error {
	if redactor == nil || err == nil {
		return err
	}
	redacted := redactAgentIdentitySensitiveText(redactor, err.Error())
	if redacted == err.Error() {
		// No credential material was removed, so retaining the original type keeps
		// ordinary context/scanner classification intact without exposing secrets.
		return err
	}
	// Never preserve the raw cause: it may itself contain credentials. Callers
	// must classify the original error before constructing this safe boundary.
	return errors.New(redacted)
}

// redactAgentIdentitySensitiveErrorBoundary deliberately drops the unwrap
// chain. Once an error crosses a log, hook, or protocol boundary, retaining a
// raw cause can re-expose credentials through errors.As/Unwrap even when the
// displayed message was redacted.
func redactAgentIdentitySensitiveErrorBoundary(redactor agentIdentityBodyRedactor, err error) error {
	if err == nil {
		return nil
	}
	if redactor == nil {
		return errors.New(err.Error())
	}
	return errors.New(redactAgentIdentitySensitiveText(redactor, err.Error()))
}

func redactAgentIdentitySensitiveDialError(redactor agentIdentityBodyRedactor, err error) error {
	if err == nil {
		return nil
	}
	var dialErr *openAIWSDialError
	if !errors.As(err, &dialErr) || dialErr == nil {
		return redactAgentIdentitySensitiveErrorBoundary(redactor, err)
	}
	safeBody := append([]byte(nil), dialErr.ResponseBody...)
	if redactor != nil {
		safeBody = redactor(safeBody)
	}
	safe := &openAIWSDialError{
		StatusCode:      dialErr.StatusCode,
		ResponseHeaders: cloneHeader(dialErr.ResponseHeaders),
		ResponseBody:    safeBody,
		Err:             redactAgentIdentitySensitiveErrorBoundary(redactor, dialErr.Err),
	}
	return safe
}
