package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/paymentproviderinstance"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/payment/provider"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// --- Refund Flow ---

var createPaymentProviderFromInstance = provider.CreateProvider

// getOrderProviderInstance looks up the provider instance that processed this order.
// For legacy orders without provider_instance_id, it resolves only when the
// historical instance is uniquely identifiable from the stored order fields.
func (s *PaymentService) getOrderProviderInstance(ctx context.Context, o *dbent.PaymentOrder) (*dbent.PaymentProviderInstance, error) {
	if s == nil || s.entClient == nil || o == nil {
		return nil, nil
	}

	if snapshot := psOrderProviderSnapshot(o); snapshot != nil {
		return s.resolveSnapshotOrderProviderInstance(ctx, o, snapshot)
	}

	instIDStr := strings.TrimSpace(psStringValue(o.ProviderInstanceID))
	if instIDStr == "" {
		return s.resolveUniqueLegacyOrderProviderInstance(ctx, o)
	}

	instID, err := strconv.ParseInt(instIDStr, 10, 64)
	if err != nil {
		return nil, nil
	}
	return s.entClient.PaymentProviderInstance.Get(ctx, instID)
}

// getRefundOrderProviderInstance resolves the provider instance for refund paths.
// Refunds must be pinned to an explicit historical binding, so legacy
// "best-effort" provider guessing is intentionally not allowed here.
func (s *PaymentService) getRefundOrderProviderInstance(ctx context.Context, o *dbent.PaymentOrder) (*dbent.PaymentProviderInstance, error) {
	if s == nil || s.entClient == nil || o == nil {
		return nil, nil
	}

	if snapshot := psOrderProviderSnapshot(o); snapshot != nil {
		return s.resolveSnapshotOrderProviderInstance(ctx, o, snapshot)
	}

	instIDStr := strings.TrimSpace(psStringValue(o.ProviderInstanceID))
	if instIDStr == "" {
		return nil, nil
	}

	instID, err := strconv.ParseInt(instIDStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("order %d refund provider instance id is invalid: %s", o.ID, instIDStr)
	}
	inst, err := s.entClient.PaymentProviderInstance.Get(ctx, instID)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, fmt.Errorf("order %d refund provider instance %s is missing", o.ID, instIDStr)
		}
		return nil, err
	}
	return inst, nil
}

func (s *PaymentService) resolveUniqueLegacyOrderProviderInstance(ctx context.Context, o *dbent.PaymentOrder) (*dbent.PaymentProviderInstance, error) {
	paymentType := payment.GetBasePaymentType(strings.TrimSpace(o.PaymentType))
	providerKey := strings.TrimSpace(psStringValue(o.ProviderKey))
	if providerKey != "" {
		instances, err := s.entClient.PaymentProviderInstance.Query().
			Where(paymentproviderinstance.ProviderKeyEQ(providerKey)).
			All(ctx)
		if err != nil {
			return nil, err
		}
		matched := psFilterLegacyOrderProviderInstances(paymentType, instances)
		if len(matched) == 1 {
			return matched[0], nil
		}
		return nil, nil
	}

	if paymentType == "" {
		return nil, nil
	}

	instances, err := s.entClient.PaymentProviderInstance.Query().
		All(ctx)
	if err != nil {
		return nil, err
	}

	matched := psFilterLegacyOrderProviderInstances(paymentType, instances)
	if len(matched) == 1 {
		return matched[0], nil
	}
	return nil, nil
}

func psFilterLegacyOrderProviderInstances(orderPaymentType string, instances []*dbent.PaymentProviderInstance) []*dbent.PaymentProviderInstance {
	if len(instances) == 0 {
		return nil
	}
	if strings.TrimSpace(orderPaymentType) == "" {
		return instances
	}
	var matched []*dbent.PaymentProviderInstance
	for _, inst := range instances {
		if psLegacyOrderMatchesInstance(orderPaymentType, inst) {
			matched = append(matched, inst)
		}
	}
	return matched
}

func psLegacyOrderMatchesInstance(orderPaymentType string, inst *dbent.PaymentProviderInstance) bool {
	if inst == nil {
		return false
	}

	baseType := payment.GetBasePaymentType(strings.TrimSpace(orderPaymentType))
	instanceProviderKey := strings.TrimSpace(inst.ProviderKey)
	if baseType == "" {
		return false
	}

	if baseType == payment.TypeStripe {
		return instanceProviderKey == payment.TypeStripe
	}
	if instanceProviderKey == payment.TypeStripe {
		return false
	}
	if instanceProviderKey == baseType {
		return true
	}
	return payment.InstanceSupportsType(inst.SupportedTypes, baseType)
}

func (s *PaymentService) RequestRefund(ctx context.Context, oid, uid int64, reason string) error {
	o, err := s.validateRefundRequest(ctx, oid, uid)
	if err != nil {
		return err
	}
	u, err := s.userRepo.GetByID(ctx, o.UserID)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if u.Balance < o.Amount {
		return infraerrors.BadRequest("BALANCE_NOT_ENOUGH", "refund amount exceeds balance")
	}
	nr := strings.TrimSpace(reason)
	now := time.Now()
	by := fmt.Sprintf("%d", uid)
	c, err := s.entClient.PaymentOrder.Update().Where(paymentorder.IDEQ(oid), paymentorder.UserIDEQ(uid), paymentorder.StatusEQ(OrderStatusCompleted), paymentorder.OrderTypeEQ(payment.OrderTypeBalance)).SetStatus(OrderStatusRefundRequested).SetRefundRequestedAt(now).SetRefundRequestReason(nr).SetRefundRequestedBy(by).SetRefundAmount(o.Amount).Save(ctx)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if c == 0 {
		return infraerrors.Conflict("CONFLICT", "order status changed")
	}
	s.writeAuditLog(ctx, oid, "REFUND_REQUESTED", fmt.Sprintf("user:%d", uid), map[string]any{"amount": o.Amount, "reason": nr})
	return nil
}

func (s *PaymentService) validateRefundRequest(ctx context.Context, oid, uid int64) (*dbent.PaymentOrder, error) {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if o.UserID != uid {
		return nil, infraerrors.Forbidden("FORBIDDEN", "no permission")
	}
	if o.OrderType != payment.OrderTypeBalance {
		return nil, infraerrors.BadRequest("INVALID_ORDER_TYPE", "only balance orders can request refund")
	}
	if o.Status != OrderStatusCompleted {
		return nil, infraerrors.BadRequest("INVALID_STATUS", "only completed orders can request refund")
	}
	// Check provider instance allows user refund
	inst, err := s.getRefundOrderProviderInstance(ctx, o)
	if err != nil || inst == nil {
		return nil, infraerrors.Forbidden("USER_REFUND_DISABLED", "refund is not available for this order")
	}
	if !inst.AllowUserRefund {
		return nil, infraerrors.Forbidden("USER_REFUND_DISABLED", "user refund is not enabled for this provider")
	}
	return o, nil
}

func (s *PaymentService) PrepareRefund(ctx context.Context, oid int64, amt float64, reason string, force, deduct bool, subDaysToDeduct int) (*RefundPlan, *RefundResult, error) {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return nil, nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	ok := []string{OrderStatusCompleted, OrderStatusRefundRequested, OrderStatusRefundFailed, OrderStatusPartiallyRefunded}
	if !psSliceContains(ok, o.Status) {
		return nil, nil, infraerrors.BadRequest("INVALID_STATUS", "order status does not allow refund")
	}
	// Check provider instance allows admin refund
	inst, instErr := s.getRefundOrderProviderInstance(ctx, o)
	if instErr != nil {
		slog.Warn("refund: provider instance lookup failed", "orderID", oid, "error", instErr)
		return nil, nil, infraerrors.InternalServer("PROVIDER_LOOKUP_FAILED", "failed to look up payment provider for this order")
	}
	if inst == nil {
		// Legacy order without provider_instance_id — block refund
		return nil, nil, infraerrors.Forbidden("REFUND_DISABLED", "refund is not available for this order")
	}
	if !inst.RefundEnabled {
		return nil, nil, infraerrors.Forbidden("REFUND_DISABLED", "refund is not enabled for this provider")
	}
	if math.IsNaN(amt) || math.IsInf(amt, 0) {
		return nil, nil, infraerrors.BadRequest("INVALID_AMOUNT", "invalid refund amount")
	}
	if amt <= 0 {
		amt = o.Amount
	}
	orderCurrency := PaymentOrderCurrency(o)
	alreadyRefunded := s.refundSuccessfulWatermark(ctx, o)
	remainingRefundable := o.Amount - alreadyRefunded
	if amt-remainingRefundable > paymentAmountToleranceForCurrency(orderCurrency) {
		return nil, nil, infraerrors.BadRequest("REFUND_AMOUNT_EXCEEDED", "refund amount exceeds recharge")
	}
	ga := calculateGatewayRefundAmount(o.Amount, o.PayAmount, amt, orderCurrency)
	rr := strings.TrimSpace(reason)
	if rr == "" && o.RefundRequestReason != nil {
		rr = *o.RefundRequestReason
	}
	if rr == "" {
		rr = fmt.Sprintf("refund order:%d", o.ID)
	}
	if !deduct {
		// The UI may retain a previously suggested subscription-day value after
		// the administrator clears the deduction checkbox. The checkbox is the
		// authoritative intent; stale subordinate values must not turn it back on.
		subDaysToDeduct = 0
	}
	p := &RefundPlan{OrderID: oid, Order: o, RefundAmount: amt, PreviousRefundAmount: alreadyRefunded, GatewayAmount: ga, Reason: rr, Force: force, DeductBalance: deduct, SubDaysToDeduct: subDaysToDeduct, DeductionType: payment.DeductionTypeNone}
	p.ProviderRefundID = refundProviderAttemptID(p)
	if o.Status == OrderStatusRefundFailed {
		// A failed provider attempt is a new tranche for idempotency purposes,
		// even when the administrator retries the same amount. Pending queries
		// continue to use the persisted ID from REFUND_PENDING.
		p.ProviderRefundID = refundProviderRetryID(p)
	}
	p.DeductionLineageID = p.ProviderRefundID
	if deduct {
		if er := s.prepDeduct(ctx, o, p, force); er != nil {
			return nil, er, nil
		}
	}
	return p, nil, nil
}

func (s *PaymentService) BuildRefundPreview(ctx context.Context, o *dbent.PaymentOrder) RefundPreview {
	if o == nil || o.OrderType != payment.OrderTypeSubscription || o.SubscriptionGroupID == nil || o.SubscriptionDays == nil || *o.SubscriptionDays <= 0 || o.Amount <= 0 {
		return RefundPreview{}
	}
	remainingDays := 0
	var expiresAt *time.Time
	foundOrderSubscription := false
	if s != nil && s.subscriptionSvc != nil {
		if sub, err := s.getRefundOrderSubscription(ctx, o); err == nil && sub != nil {
			foundOrderSubscription = true
			remainingDays = daysRemainingFromNow(sub.ExpiresAt)
			exp := sub.ExpiresAt
			expiresAt = &exp
		}
	}
	if remainingDays <= 0 && !foundOrderSubscription {
		remainingDays = estimateOrderSubscriptionRemainingDays(o)
	}
	if remainingDays <= 0 {
		return RefundPreview{SubscriptionExpiresAt: expiresAt}
	}
	if remainingDays > *o.SubscriptionDays {
		remainingDays = *o.SubscriptionDays
	}
	refundAmount := calculateSubscriptionRefundAmountByDays(*o.SubscriptionDays, o.Amount, remainingDays)
	if o.Status == OrderStatusPartiallyRefunded {
		refundAmount = math.Min(refundAmount, math.Max(0, o.Amount-o.RefundAmount))
	}
	return RefundPreview{
		SubscriptionRemainingDays:         remainingDays,
		SubscriptionExpiresAt:             expiresAt,
		SuggestedRefundAmount:             refundAmount,
		SuggestedSubscriptionDaysToDeduct: remainingDays,
	}
}

func (s *PaymentService) prepDeduct(ctx context.Context, o *dbent.PaymentOrder, p *RefundPlan, force bool) *RefundResult {
	if o.OrderType == payment.OrderTypeSubscription {
		p.DeductionType = payment.DeductionTypeSubscription
		if o.SubscriptionGroupID == nil || o.SubscriptionDays == nil || *o.SubscriptionDays <= 0 {
			// Malformed legacy subscription metadata cannot identify an entitlement
			// to deduct. Never carry caller-provided days into the audit as a
			// phantom mutation; require explicit force to proceed without one.
			p.SubDaysToDeduct = 0
			if !force {
				return &RefundResult{Success: false, Warning: "subscription metadata is incomplete, use force", RequireForce: true}
			}
			return nil
		}
		if p.SubDaysToDeduct <= 0 {
			p.SubDaysToDeduct = calculateSubscriptionRefundDays(*o.SubscriptionDays, o.Amount, p.RefundAmount)
		}
		if p.SubDaysToDeduct > *o.SubscriptionDays {
			p.SubDaysToDeduct = *o.SubscriptionDays
		}
		sub, err := s.getRefundOrderSubscription(ctx, o)
		if err == nil && sub != nil {
			p.SubscriptionID = sub.ID
		} else if !force {
			return &RefundResult{Success: false, Warning: "cannot find active subscription for deduction, use force", RequireForce: true}
		} else {
			// Force permits the gateway refund to proceed when the historical
			// entitlement no longer exists, but no subscription mutation was
			// actually applied. Do not persist a phantom deduction that later
			// recovery would wait on forever.
			p.SubDaysToDeduct = 0
		}
		return nil
	}
	u, err := s.userRepo.GetByID(ctx, o.UserID)
	if err != nil {
		if !force {
			return &RefundResult{Success: false, Warning: "cannot fetch user balance, use force", RequireForce: true}
		}
		return nil
	}
	p.DeductionType = payment.DeductionTypeBalance
	if u.Balance < p.RefundAmount && !force {
		return &RefundResult{Success: false, Warning: "user balance is insufficient for deduction, use force", RequireForce: true}
	}
	p.BalanceToDeduct = math.Max(0, math.Min(p.RefundAmount, u.Balance))
	return nil
}

func (s *PaymentService) getRefundOrderSubscription(ctx context.Context, o *dbent.PaymentOrder) (*UserSubscription, error) {
	if s == nil || s.subscriptionSvc == nil || o == nil || o.SubscriptionGroupID == nil {
		return nil, ErrSubscriptionNotFound
	}
	if o.FulfilledSubscriptionID != nil && *o.FulfilledSubscriptionID > 0 {
		sub, err := s.subscriptionSvc.GetByID(ctx, *o.FulfilledSubscriptionID)
		if err != nil {
			return nil, err
		}
		if sub.UserID == o.UserID && sub.GroupID == *o.SubscriptionGroupID {
			return sub, nil
		}
		return nil, ErrSubscriptionNotFound
	}
	sub, err := s.subscriptionSvc.GetSubscriptionByPaymentOrder(ctx, o.UserID, *o.SubscriptionGroupID, o.ID)
	if err == nil && sub != nil {
		return sub, nil
	}
	if err != nil && !errors.Is(err, ErrSubscriptionNotFound) {
		return nil, err
	}
	return s.subscriptionSvc.GetActiveSubscription(ctx, o.UserID, *o.SubscriptionGroupID)
}

func calculateSubscriptionRefundAmountByDays(orderDays int, orderAmount float64, days int) float64 {
	if orderDays <= 0 || orderAmount <= 0 || days <= 0 {
		return 0
	}
	if days > orderDays {
		days = orderDays
	}
	return decimal.NewFromFloat(orderAmount).
		Mul(decimal.NewFromInt(int64(days))).
		Div(decimal.NewFromInt(int64(orderDays))).
		Shift(2).
		Floor().
		Shift(-2).
		InexactFloat64()
}

func calculateSubscriptionRefundDays(orderDays int, orderAmount, refundAmount float64) int {
	if orderDays <= 0 || orderAmount <= 0 || refundAmount <= 0 {
		return 0
	}
	days := int(math.Ceil(float64(orderDays) * refundAmount / orderAmount))
	if days < 1 {
		return 1
	}
	if days > orderDays {
		return orderDays
	}
	return days
}

func estimateOrderSubscriptionRemainingDays(o *dbent.PaymentOrder) int {
	if o == nil || o.SubscriptionDays == nil || *o.SubscriptionDays <= 0 {
		return 0
	}
	start := o.CompletedAt
	if start == nil {
		start = o.PaidAt
	}
	if start == nil {
		start = &o.CreatedAt
	}
	expiresAt := start.Add(time.Duration(*o.SubscriptionDays) * 24 * time.Hour)
	return daysRemainingFromNow(expiresAt)
}

type availableBalanceDeductor interface {
	DeductAvailableBalance(ctx context.Context, id int64, amount float64) (float64, error)
}

func (s *PaymentService) deductAvailableBalance(ctx context.Context, userID int64, amount float64) (float64, error) {
	repo, ok := s.userRepo.(availableBalanceDeductor)
	if !ok {
		return 0, errors.New("user repository does not support available balance deduction")
	}
	return repo.DeductAvailableBalance(ctx, userID, amount)
}

// applyRefundInitialDeduction performs the entitlement mutation for a newly
// claimed refund. It is called inside the same DB transaction that persists the
// REFUND_ATTEMPT marker, so a process crash cannot leave a deduction with no
// durable lineage.
func (s *PaymentService) applyRefundInitialDeduction(ctx context.Context, p *RefundPlan) error {
	if p == nil || p.DeductionAlreadyApplied {
		return nil
	}
	if p.DeductionType == payment.DeductionTypeBalance && p.BalanceToDeduct > 0 {
		deducted, err := s.deductAvailableBalance(ctx, p.Order.UserID, p.BalanceToDeduct)
		if err != nil {
			return fmt.Errorf("deduction: %w", err)
		}
		p.BalanceToDeduct = deducted
	}
	if p.DeductionType == payment.DeductionTypeSubscription && p.SubDaysToDeduct > 0 && p.SubscriptionID > 0 {
		if s.subscriptionSvc == nil {
			return errors.New("subscription deduction service unavailable")
		}
		if _, err := s.subscriptionSvc.extendSubscription(ctx, p.SubscriptionID, -p.SubDaysToDeduct, true); err != nil {
			if !errors.Is(err, ErrAdjustWouldExpire) {
				return fmt.Errorf("deduct subscription days: %w", err)
			}
			if _, revokeErr := s.subscriptionSvc.revokeSubscription(ctx, p.SubscriptionID, true); revokeErr != nil {
				return fmt.Errorf("revoke subscription: %w", revokeErr)
			}
			p.SubscriptionRevoked = true
		}
	}
	return nil
}

func (s *PaymentService) invalidateRefundSubscriptionCaches(o *dbent.PaymentOrder, subscriptionID int64, mutated bool) {
	if !mutated || s == nil || s.subscriptionSvc == nil || o == nil || subscriptionID <= 0 || o.SubscriptionGroupID == nil {
		return
	}
	s.subscriptionSvc.invalidateSubscriptionCaches(o.UserID, *o.SubscriptionGroupID)
}

func (s *PaymentService) applyRefundInitialDeductionAndPersist(ctx context.Context, p *RefundPlan) error {
	if s == nil || s.entClient == nil {
		return errors.New("payment database unavailable")
	}
	if dbent.TxFromContext(ctx) != nil {
		return errors.New("refund attempt must own its transaction")
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin refund attempt: %w", err)
	}
	txCtx := dbent.NewTxContext(ctx, tx)
	if err := s.applyRefundInitialDeduction(txCtx, p); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := s.persistRefundAttempt(txCtx, tx.Client(), p); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("persist refund attempt: %w", err)
	}
	commitErr := tx.Commit()
	s.invalidateRefundSubscriptionCaches(p.Order, p.SubscriptionID, p.SubDaysToDeduct > 0)
	if commitErr != nil {
		return &refundAttemptCommitOutcomeUnknownError{err: commitErr}
	}
	return nil
}

func (s *PaymentService) ExecuteRefund(ctx context.Context, p *RefundPlan) (*RefundResult, error) {
	c, err := s.entClient.PaymentOrder.Update().Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusIn(OrderStatusCompleted, OrderStatusRefundRequested, OrderStatusRefundFailed, OrderStatusPartiallyRefunded)).SetStatus(OrderStatusRefunding).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	if c == 0 {
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}
	if p.ProviderRefundID == "" {
		p.ProviderRefundID = refundProviderAttemptID(p)
	}
	if p.DeductionLineageID == "" {
		p.DeductionLineageID = p.ProviderRefundID
	}
	if applied, ok, lineageErr := s.latestUnresolvedRefundRollback(ctx, p); lineageErr != nil {
		s.restoreStatus(ctx, p)
		return nil, infraerrors.Conflict("REFUND_ROLLBACK_UNRESOLVED", lineageErr.Error())
	} else if ok {
		p.DeductionAlreadyApplied = true
		p.DeductionLineageID = applied.deductionLineageID()
		p.SubscriptionRevoked = applied.SubscriptionRevoked
		if applied.BalanceDeducted > 0 {
			p.BalanceToDeduct = applied.BalanceDeducted
		}
		if applied.SubDaysDeducted > 0 {
			p.SubDaysToDeduct = applied.SubDaysDeducted
		}
		if applied.SubscriptionID > 0 {
			p.SubscriptionID = applied.SubscriptionID
		}
	}
	if err := s.applyRefundInitialDeductionAndPersist(ctx, p); err != nil {
		return nil, s.handleRefundAttemptPreparationError(ctx, p, err)
	}
	resp, err := s.gwRefund(ctx, p)
	if err != nil {
		if isRefundGatewayOutcomeUnknown(err) {
			// The remote call may have reached the provider despite a local timeout.
			// Never retry it by rolling status back; retain a durable request id and
			// move to manual/provider-query recovery instead.
			s.writeAuditLog(ctx, p.OrderID, "REFUND_GATEWAY_OUTCOME_UNKNOWN", "admin", map[string]any{
				"detail":    psErrMsg(err),
				"attemptID": p.ProviderRefundID,
			})
			// ProviderRefundID is a local idempotency key, not necessarily an
			// upstream refund resource ID. Leave RefundID empty so the follow-up
			// query uses the provider's owner-bound deterministic fallback.
			return s.markRefundPending(ctx, p, &payment.RefundResponse{Status: payment.ProviderStatusPending})
		}
		return s.handleGwFail(ctx, p, err, false)
	}
	return s.finishRefund(ctx, p, resp)
}

func (s *PaymentService) handleRefundAttemptPreparationError(ctx context.Context, p *RefundPlan, err error) error {
	if isRefundAttemptCommitOutcomeUnknown(err) {
		// COMMIT can succeed on the database while its acknowledgement is lost.
		// Restoring the order here would make the same entitlement deductible by
		// a second attempt. Keep REFUNDING until an operator verifies the durable
		// attempt marker and entitlement state.
		s.writeAuditLog(ctx, p.OrderID, "REFUND_ATTEMPT_COMMIT_UNKNOWN", "admin", map[string]any{
			"attemptID":           p.ProviderRefundID,
			"deductionLineageID":  p.deductionLineageID(),
			"balanceDeducted":     p.BalanceToDeduct,
			"subDaysDeducted":     p.SubDaysToDeduct,
			"subscriptionID":      p.SubscriptionID,
			"subscriptionRevoked": p.SubscriptionRevoked,
			"detail":              psErrMsg(err),
		})
		return infraerrors.InternalServer(
			"REFUND_ATTEMPT_COMMIT_UNKNOWN",
			"refund preparation result is uncertain; verify the order before retrying",
		).WithCause(err)
	}
	s.restoreStatus(ctx, p)
	return fmt.Errorf("prepare refund attempt: %w", err)
}

type refundGatewayOutcomeUnknownError struct {
	err error
}

func (e *refundGatewayOutcomeUnknownError) Error() string {
	return e.err.Error()
}

func (e *refundGatewayOutcomeUnknownError) Unwrap() error {
	return e.err
}

func markRefundGatewayOutcomeUnknown(err error) error {
	if err == nil {
		return nil
	}
	return &refundGatewayOutcomeUnknownError{err: err}
}

func isRefundGatewayOutcomeUnknown(err error) bool {
	var target *refundGatewayOutcomeUnknownError
	return errors.As(err, &target)
}

// refundAttemptCommitOutcomeUnknownError means the transaction outcome cannot
// be inferred from the COMMIT error. Callers must not restore or retry the order
// until the persisted attempt and entitlement state have been verified.
type refundAttemptCommitOutcomeUnknownError struct {
	err error
}

func (e *refundAttemptCommitOutcomeUnknownError) Error() string {
	return fmt.Sprintf("commit refund attempt: %v", e.err)
}

func (e *refundAttemptCommitOutcomeUnknownError) Unwrap() error {
	return e.err
}

func isRefundAttemptCommitOutcomeUnknown(err error) bool {
	var target *refundAttemptCommitOutcomeUnknownError
	return errors.As(err, &target)
}

func (s *PaymentService) gwRefund(ctx context.Context, p *RefundPlan) (*payment.RefundResponse, error) {
	if p.ProviderRefundID == "" {
		p.ProviderRefundID = refundProviderAttemptID(p)
	}
	if p.Order.PaymentTradeNo == "" {
		s.writeAuditLog(ctx, p.Order.ID, "REFUND_NO_TRADE_NO", "admin", map[string]any{"detail": "skipped"})
		return &payment.RefundResponse{Status: payment.ProviderStatusSuccess}, nil
	}

	// Use the exact provider instance that created this order, not a random one
	// from the registry. Each instance has its own merchant credentials.
	prov, err := s.getRefundProvider(ctx, p.Order)
	if err != nil {
		return nil, fmt.Errorf("get refund provider: %w", err)
	}
	if err := validateProviderSnapshotMetadata(p.Order, prov.ProviderKey(), providerMerchantIdentityMetadata(prov)); err != nil {
		s.writeAuditLog(ctx, p.Order.ID, "REFUND_PROVIDER_METADATA_MISMATCH", "admin", map[string]any{
			"detail": err.Error(),
		})
		return nil, err
	}
	finishProviderCall := servertiming.ObserveDependency(ctx, "payment")
	resp, err := prov.Refund(ctx, payment.RefundRequest{
		TradeNo:  p.Order.PaymentTradeNo,
		OrderID:  p.Order.OutTradeNo,
		RefundID: p.ProviderRefundID,
		Amount:   formatGatewayRefundAmount(p.GatewayAmount, p.Order),
		Reason:   p.Reason,
	})
	finishProviderCall()
	if resp != nil {
		p.ProviderQueryID = strings.TrimSpace(resp.RefundID)
		switch strings.TrimSpace(resp.Status) {
		case payment.ProviderStatusSuccess, payment.ProviderStatusRefunded, payment.ProviderStatusPending, payment.ProviderStatusFailed:
			return resp, nil
		}
	}
	if err != nil {
		return nil, markRefundGatewayOutcomeUnknown(err)
	}
	if resp == nil {
		return nil, markRefundGatewayOutcomeUnknown(errors.New("payment refund response missing"))
	}
	return nil, markRefundGatewayOutcomeUnknown(fmt.Errorf("payment refund returned unknown status: %s", strings.TrimSpace(resp.Status)))
}

func formatGatewayRefundAmount(amount float64, order *dbent.PaymentOrder) string {
	return payment.FormatAmountForCurrency(amount, PaymentOrderCurrency(order))
}

// refundProviderAttemptID is stable for the same order/refund tranche and
// changes for later partial refunds. Its compact hash fits provider request-id
// limits without exposing merchant order identifiers.
func refundProviderAttemptID(p *RefundPlan) string {
	if p == nil {
		return ""
	}
	orderID := strconv.FormatInt(p.OrderID, 10)
	if p.Order != nil && strings.TrimSpace(p.Order.OutTradeNo) != "" {
		orderID = strings.TrimSpace(p.Order.OutTradeNo)
	}
	payload := strings.Join([]string{orderID, formatGatewayRefundAmount(p.GatewayAmount, p.Order), strconv.FormatFloat(p.PreviousRefundAmount, 'f', -1, 64)}, "|")
	digest := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("rf_%x", digest[:12])
}

func refundProviderRetryID(p *RefundPlan) string {
	if p == nil {
		return ""
	}
	digest := sha256.Sum256([]byte(refundProviderAttemptID(p) + "|" + uuid.NewString()))
	return fmt.Sprintf("rf_%x", digest[:12])
}

func validateRefundProviderResponse(resp *payment.RefundResponse) error {
	if resp == nil {
		return fmt.Errorf("payment refund response missing")
	}
	status := strings.TrimSpace(resp.Status)
	switch status {
	case payment.ProviderStatusSuccess, payment.ProviderStatusRefunded, payment.ProviderStatusPending:
		return nil
	case payment.ProviderStatusFailed:
		return fmt.Errorf("payment refund failed: status %s", status)
	default:
		return fmt.Errorf("payment refund returned unknown status: %s", status)
	}
}

func (s *PaymentService) finishRefund(ctx context.Context, p *RefundPlan, resp *payment.RefundResponse) (*RefundResult, error) {
	if p != nil && resp != nil {
		p.ProviderQueryID = strings.TrimSpace(resp.RefundID)
	}
	if err := validateRefundProviderResponse(resp); err != nil {
		return s.handleGwFail(ctx, p, err, strings.TrimSpace(resp.Status) == payment.ProviderStatusFailed)
	}
	switch strings.TrimSpace(resp.Status) {
	case payment.ProviderStatusSuccess, payment.ProviderStatusRefunded:
		return s.markRefundOk(ctx, p)
	case payment.ProviderStatusPending:
		return s.markRefundPending(ctx, p, resp)
	default:
		return s.handleGwFail(ctx, p, fmt.Errorf("payment refund returned unknown status: %s", strings.TrimSpace(resp.Status)), false)
	}
}

func (s *PaymentService) QueryAndFinalizeRefund(ctx context.Context, oid int64) (*RefundResult, error) {
	o, err := s.entClient.PaymentOrder.Get(ctx, oid)
	if err != nil {
		return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if o.Status != OrderStatusRefundPending && o.Status != OrderStatusRefunding {
		return nil, infraerrors.BadRequest("INVALID_STATUS", "only refund pending or interrupted orders can be finalized")
	}

	prov, err := s.getRefundProvider(ctx, o)
	if err != nil {
		return nil, fmt.Errorf("get refund provider: %w", err)
	}
	queryProvider, ok := prov.(payment.RefundQueryProvider)
	if !ok {
		return nil, infraerrors.BadRequest("REFUND_QUERY_UNSUPPORTED", "this payment provider does not support refund status query; please verify manually")
	}

	pendingDetail := s.latestRefundPendingDetail(ctx, oid)
	if o.Status == OrderStatusRefunding {
		pendingDetail = s.latestRefundAttemptDetail(ctx, oid)
	}
	queryRefundID := refundProviderQueryID(pendingDetail.RefundID)
	if strings.TrimSpace(pendingDetail.AttemptID) != "" &&
		strings.TrimSpace(pendingDetail.AttemptID) == strings.TrimSpace(pendingDetail.RefundID) {
		queryRefundID = ""
	}
	queryByOrder, supportsQueryByOrder := prov.(payment.RefundQueryByOrderProvider)
	if queryRefundID == "" && !supportsQueryByOrder {
		// A local idempotency key is not a provider resource identifier. If the
		// provider has no owner-bound fallback lookup, keep the order actionable
		// for manual verification rather than issuing an ambiguous request.
		return nil, infraerrors.Conflict("REFUND_ID_MISSING", "refund attempt identifier is unavailable; verify the gateway transaction manually")
	}
	pendingAmount := pendingDetail.pendingRefundAmount(o)
	finishProviderCall := servertiming.ObserveDependency(ctx, "payment")
	queryRequest := payment.RefundQueryRequest{
		TradeNo:   o.PaymentTradeNo,
		OrderID:   o.OutTradeNo,
		RefundID:  queryRefundID,
		AttemptID: strings.TrimSpace(pendingDetail.AttemptID),
		Amount:    formatGatewayRefundAmount(calculateGatewayRefundAmount(o.Amount, o.PayAmount, pendingAmount, PaymentOrderCurrency(o)), o),
	}
	var resp *payment.RefundResponse
	if queryRefundID == "" {
		resp, err = queryByOrder.QueryRefundByOrder(ctx, queryRequest)
	} else {
		resp, err = queryProvider.QueryRefund(ctx, queryRequest)
	}
	finishProviderCall()
	if err != nil {
		return nil, fmt.Errorf("query refund: %w", err)
	}
	providerResultErr := validateRefundProviderResponse(resp)
	if !pendingDetail.DeductionStateKnown {
		s.writeAuditLog(ctx, oid, "REFUND_QUERY_DEDUCTION_STATE_UNKNOWN", "admin", map[string]any{
			"refundID": refundResponseID(resp),
			"status":   strings.TrimSpace(refundResponseStatus(resp)),
			"detail":   psErrMsg(providerResultErr),
		})
		return nil, infraerrors.Conflict(
			"REFUND_DEDUCTION_STATE_UNKNOWN",
			"refund provider status was queried, but the entitlement deduction state requires manual verification",
		)
	}
	if providerResultErr != nil {
		if pendingDetail.deductionNeedsRollbackRecovery() {
			pendingDetail, recoveryDetailErr := s.resolveLegacyRefundSubscriptionDetail(ctx, o, pendingDetail)
			if recoveryDetailErr != nil {
				return nil, infraerrors.Conflict("REFUND_ROLLBACK_UNRESOLVED", recoveryDetailErr.Error())
			}
			if validRefundAuditAmount(pendingDetail.BalanceDeducted) == 0 && pendingDetail.SubDaysDeducted <= 0 {
				// A provider-success result needs no compensation and may safely
				// finalize with a legacy amount-less marker. A failed provider result
				// cannot be compensated without the exact durable amount.
				return nil, infraerrors.Conflict("REFUND_ROLLBACK_UNRESOLVED", "refund deduction amount is unavailable; verify entitlement manually")
			}
			recovered, recoveryErr := s.recoverRefundDeduction(ctx, o, pendingDetail, providerResultErr)
			if !recovered {
				s.writeAuditLog(ctx, oid, "REFUND_ROLLBACK_PENDING", "admin", map[string]any{
					"refundID": pendingDetail.RefundID,
					"detail":   psErrMsg(recoveryErr),
				})
				return &RefundResult{Success: false, Warning: "gateway refund failed; entitlement rollback is still pending"}, nil
			}
			return &RefundResult{Success: false, Warning: "gateway refund failed; deduction rollback recovered"}, nil
		}
		return s.finalizeRefundFailed(ctx, o, providerResultErr)
	}
	if providerID := refundProviderQueryID(refundResponseID(resp)); providerID != "" {
		pendingDetail.RefundID = providerID
	}

	plan := s.refundFinalizePlan(o, pendingDetail)
	if pendingDetail.DeductionApplied || !pendingDetail.DeductionRollbackOK {
		plan.DeductionAlreadyApplied = true
		plan.BalanceToDeduct = pendingDetail.BalanceDeducted
		plan.SubDaysToDeduct = pendingDetail.SubDaysDeducted
		plan.SubscriptionID = pendingDetail.SubscriptionID
		if plan.SubDaysToDeduct > 0 {
			plan.DeductionType = payment.DeductionTypeSubscription
		}
	} else if pendingDetail.DeductionRequested != nil {
		// New audit rows carry the exact administrator choice and exact amount
		// that was previously deducted and rolled back. A recorded zero is
		// authoritative; do not recalculate against a balance that may have
		// changed while the provider refund was pending.
	} else if o.OrderType == payment.OrderTypeSubscription {
		if pendingDetail.SubDaysDeducted > 0 {
			plan.SubDaysToDeduct = pendingDetail.SubDaysDeducted
			plan.SubscriptionID = pendingDetail.SubscriptionID
			plan.DeductionType = payment.DeductionTypeSubscription
		} else if early := s.prepDeduct(ctx, o, plan, true); early != nil {
			return early, nil
		}
	} else if pendingDetail.BalanceDeducted > 0 {
		// A pending attempt may have been clamped to the user's available
		// balance. Reuse that durable amount after rollback instead of charging
		// the full requested refund on confirmation.
		plan.BalanceToDeduct = pendingDetail.BalanceDeducted
	}
	switch strings.TrimSpace(resp.Status) {
	case payment.ProviderStatusSuccess, payment.ProviderStatusRefunded:
		return s.finalizeRefundSuccessFromStatus(ctx, plan, o.Status)
	case payment.ProviderStatusPending:
		pendingDetail.RefundID = strings.TrimSpace(refundResponseID(resp))
		if pendingDetail.RefundID == "" {
			pendingDetail.RefundID = queryRefundID
		}
		s.persistRefundQueryPendingAudit(ctx, oid, pendingDetail)
		s.writeAuditLog(ctx, oid, "REFUND_QUERY_PENDING", "admin", map[string]any{"refundID": pendingDetail.RefundID, "attemptID": pendingDetail.AttemptID})
		return &RefundResult{Success: false, Warning: "gateway refund is still pending confirmation"}, nil
	default:
		return s.finalizeRefundFailed(ctx, o, fmt.Errorf("payment refund returned unknown status: %s", strings.TrimSpace(resp.Status)))
	}
}

func (s *PaymentService) resolveLegacyRefundSubscriptionDetail(
	ctx context.Context,
	o *dbent.PaymentOrder,
	detail refundPendingAuditDetail,
) (refundPendingAuditDetail, error) {
	if detail.SubDaysDeducted <= 0 || detail.SubscriptionID > 0 {
		return detail, nil
	}
	if s == nil || s.subscriptionSvc == nil || s.subscriptionSvc.userSubRepo == nil || o == nil ||
		o.FulfilledSubscriptionID == nil || *o.FulfilledSubscriptionID <= 0 || o.SubscriptionGroupID == nil {
		return detail, errors.New("refund subscription identifier is unavailable; verify entitlement manually")
	}
	sub, err := s.subscriptionSvc.userSubRepo.GetByIDIncludeDeleted(ctx, *o.FulfilledSubscriptionID)
	if err != nil {
		return detail, fmt.Errorf("resolve refund subscription: %w", err)
	}
	if sub == nil || sub.UserID != o.UserID || sub.GroupID != *o.SubscriptionGroupID {
		return detail, errors.New("refund subscription does not match the payment order; verify entitlement manually")
	}
	detail.SubscriptionID = sub.ID
	detail.SubscriptionRevoked = sub.DeletedAt != nil
	return detail, nil
}

func (s *PaymentService) finalizeRefundSuccessFromStatus(ctx context.Context, p *RefundPlan, expectedStatus string) (_ *RefundResult, err error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin refund finalization: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	txCtx := dbent.NewTxContext(ctx, tx)

	claimed, err := tx.PaymentOrder.Update().
		Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusEQ(expectedStatus)).
		SetStatus(OrderStatusRefunding).
		Save(txCtx)
	if err != nil {
		return nil, fmt.Errorf("claim pending refund: %w", err)
	}
	if claimed == 0 {
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}

	if err := s.applyRefundFinalDeduction(txCtx, p); err != nil {
		return nil, err
	}
	result, err := s.markRefundOkTx(txCtx, tx.Client(), p)
	if err != nil {
		return nil, err
	}
	err = tx.Commit()
	s.invalidateRefundSubscriptionCaches(p.Order, p.SubscriptionID, p.SubDaysToDeduct > 0)
	if err != nil {
		return nil, fmt.Errorf("commit refund finalization: %w", err)
	}
	return result, nil
}

func (s *PaymentService) refundFinalizePlan(o *dbent.PaymentOrder, pendingDetail refundPendingAuditDetail) *RefundPlan {
	refundAmount := pendingDetail.pendingRefundAmount(o)
	previousRefundAmount := validRefundAuditAmount(pendingDetail.PreviousRefundAmount)
	deductionRequested := pendingDetail.deductionWasRequested()
	deductionType := pendingDetail.deductionTypeForOrder(o)
	reason := strings.TrimSpace(psStringValue(o.RefundReason))
	if reason == "" {
		reason = fmt.Sprintf("refund order:%d", o.ID)
	}
	return &RefundPlan{
		OrderID:              o.ID,
		Order:                o,
		RefundAmount:         refundAmount,
		PreviousRefundAmount: previousRefundAmount,
		GatewayAmount:        calculateGatewayRefundAmount(o.Amount, o.PayAmount, refundAmount, PaymentOrderCurrency(o)),
		Reason:               reason,
		ProviderRefundID:     strings.TrimSpace(pendingDetail.AttemptID),
		ProviderQueryID:      strings.TrimSpace(pendingDetail.RefundID),
		DeductionLineageID:   pendingDetail.deductionLineageID(),
		SubscriptionRevoked:  pendingDetail.SubscriptionRevoked,
		Force:                o.ForceRefund,
		DeductBalance:        deductionRequested,
		DeductionType:        deductionType,
		BalanceToDeduct: func() float64 {
			if !deductionRequested || deductionType != payment.DeductionTypeBalance {
				return 0
			}
			if pendingDetail.DeductionRequested != nil {
				return validRefundAuditAmount(pendingDetail.BalanceDeducted)
			}
			if o.OrderType == payment.OrderTypeBalance {
				return refundAmount
			}
			return 0
		}(),
		SubDaysToDeduct: func() int {
			if deductionRequested && deductionType == payment.DeductionTypeSubscription && pendingDetail.DeductionRequested != nil {
				return pendingDetail.SubDaysDeducted
			}
			return 0
		}(),
		SubscriptionID: pendingDetail.SubscriptionID,
	}
}

func (s *PaymentService) applyRefundFinalDeduction(ctx context.Context, p *RefundPlan) error {
	if p.DeductionAlreadyApplied {
		return nil
	}
	if p.DeductionType == payment.DeductionTypeBalance && p.BalanceToDeduct > 0 {
		deducted, err := s.deductAvailableBalance(ctx, p.Order.UserID, p.BalanceToDeduct)
		if err != nil {
			return fmt.Errorf("deduction: %w", err)
		}
		p.BalanceToDeduct = deducted
	}
	if p.DeductionType == payment.DeductionTypeSubscription && p.SubDaysToDeduct > 0 && p.SubscriptionID > 0 {
		if _, err := s.subscriptionSvc.extendSubscription(ctx, p.SubscriptionID, -p.SubDaysToDeduct, true); err != nil {
			if errors.Is(err, ErrAdjustWouldExpire) {
				if _, revokeErr := s.subscriptionSvc.revokeSubscription(ctx, p.SubscriptionID, true); revokeErr != nil {
					return fmt.Errorf("revoke subscription: %w", revokeErr)
				}
				p.SubscriptionRevoked = true
			} else {
				return fmt.Errorf("deduct subscription days: %w", err)
			}
		}
	}
	return nil
}

func (s *PaymentService) finalizeRefundFailed(ctx context.Context, o *dbent.PaymentOrder, gErr error) (*RefundResult, error) {
	now := time.Now()
	updated, err := s.entClient.PaymentOrder.Update().
		Where(paymentorder.IDEQ(o.ID), paymentorder.StatusEQ(o.Status)).
		SetStatus(OrderStatusRefundFailed).SetFailedAt(now).SetFailedReason(psErrMsg(gErr)).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("mark refund failed: %w", err)
	}
	if updated == 0 {
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}
	s.writeAuditLog(ctx, o.ID, "REFUND_FAILED", "admin", map[string]any{"detail": psErrMsg(gErr)})
	return &RefundResult{Success: false, Warning: "gateway refund failed: " + psErrMsg(gErr)}, nil
}

func (s *PaymentService) persistRefundQueryPendingAudit(ctx context.Context, oid int64, detail refundPendingAuditDetail) {
	// Keep the provider resource ID discovered by a status query in the same
	// canonical action that the next retry reads. Otherwise a later retry would
	// fall back to an order-wide search and could select a different tranche.
	s.writeAuditLog(ctx, oid, "REFUND_PENDING", "admin", map[string]any{
		"refundID":             detail.RefundID,
		"attemptID":            detail.AttemptID,
		"deductionLineageID":   detail.DeductionLineageID,
		"refundAmount":         detail.RefundAmount,
		"previousRefundAmount": detail.PreviousRefundAmount,
		"totalRefundAmount":    detail.TotalRefundAmount,
		"deductionRollbackOK":  detail.DeductionRollbackOK,
		"deductionApplied":     detail.DeductionApplied,
		"deductionStateKnown":  detail.DeductionStateKnown,
		"deductionRequested":   detail.DeductionRequested,
		"deductionType":        detail.DeductionType,
		"balanceDeducted":      detail.BalanceDeducted,
		"subDaysDeducted":      detail.SubDaysDeducted,
		"subscriptionID":       detail.SubscriptionID,
		"subscriptionRevoked":  detail.SubscriptionRevoked,
	})
}

type refundPendingAuditDetail struct {
	RefundID             string  `json:"refundID"`
	AttemptID            string  `json:"attemptID,omitempty"`
	DeductionLineageID   string  `json:"deductionLineageID"`
	RefundAmount         float64 `json:"refundAmount"`
	PreviousRefundAmount float64 `json:"previousRefundAmount"`
	TotalRefundAmount    float64 `json:"totalRefundAmount"`
	DeductionRollbackOK  bool    `json:"deductionRollbackOK"`
	DeductionApplied     bool    `json:"deductionApplied"`
	DeductionStateKnown  bool    `json:"deductionStateKnown,omitempty"`
	// DeductionRequested is a pointer so legacy audit rows that predate this
	// field remain distinguishable from an explicit administrator choice of
	// false. Legacy rows retain the historical default; new rows preserve the
	// exact request and exact amount, including a clamped zero deduction.
	DeductionRequested  *bool   `json:"deductionRequested,omitempty"`
	DeductionType       string  `json:"deductionType,omitempty"`
	BalanceDeducted     float64 `json:"balanceDeducted"`
	SubDaysDeducted     int     `json:"subDaysDeducted"`
	SubscriptionID      int64   `json:"subscriptionID"`
	SubscriptionRevoked bool    `json:"subscriptionRevoked,omitempty"`
}

func (p *RefundPlan) deductionLineageID() string {
	if p == nil {
		return ""
	}
	if lineage := strings.TrimSpace(p.DeductionLineageID); lineage != "" {
		return lineage
	}
	return strings.TrimSpace(p.ProviderRefundID)
}

func (s *PaymentService) persistRefundAttempt(ctx context.Context, client *dbent.Client, p *RefundPlan) error {
	if client == nil {
		return errors.New("payment database unavailable")
	}
	deductionApplied := p.DeductionAlreadyApplied || p.BalanceToDeduct > 0 || p.SubDaysToDeduct > 0
	deductionRequested := refundDeductionWasRequested(p)
	detail, err := json.Marshal(refundPendingAuditDetail{
		// The provider query ID is empty until the create call returns. The
		// local attempt key belongs in AttemptID and must never be sent to a
		// provider's resource lookup endpoint.
		RefundID:             strings.TrimSpace(p.ProviderQueryID),
		AttemptID:            strings.TrimSpace(p.ProviderRefundID),
		DeductionLineageID:   p.deductionLineageID(),
		RefundAmount:         p.RefundAmount,
		PreviousRefundAmount: refundPreviousAmount(p),
		TotalRefundAmount:    refundPreviousAmount(p) + p.RefundAmount,
		// The attempt is persisted before the provider call and before any
		// rollback can run. A crash in that window must leave a durable marker
		// that recovery can use to restore the deduction exactly once.
		DeductionRollbackOK: !deductionApplied,
		DeductionApplied:    deductionApplied,
		DeductionStateKnown: true,
		DeductionRequested:  &deductionRequested,
		DeductionType:       refundPlanDeductionType(p, deductionRequested),
		BalanceDeducted:     p.BalanceToDeduct,
		SubDaysDeducted:     p.SubDaysToDeduct,
		SubscriptionID:      p.SubscriptionID,
		SubscriptionRevoked: p.SubscriptionRevoked,
	})
	if err != nil {
		return err
	}
	_, err = client.PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(p.OrderID, 10)).SetAction("REFUND_ATTEMPT").SetDetail(string(detail)).SetOperator("admin").Save(ctx)
	return err
}

func refundDeductionWasRequested(p *RefundPlan) bool {
	return p != nil && (p.DeductBalance || p.DeductionAlreadyApplied || p.BalanceToDeduct > 0 || p.SubDaysToDeduct > 0)
}

func refundPlanDeductionType(p *RefundPlan, requested bool) string {
	if !requested || p == nil {
		return payment.DeductionTypeNone
	}
	switch p.DeductionType {
	case payment.DeductionTypeBalance, payment.DeductionTypeSubscription:
		return p.DeductionType
	}
	if p.Order != nil && p.Order.OrderType == payment.OrderTypeSubscription {
		return payment.DeductionTypeSubscription
	}
	return payment.DeductionTypeBalance
}

func (d refundPendingAuditDetail) deductionWasRequested() bool {
	if d.DeductionRequested != nil {
		return *d.DeductionRequested
	}
	// Old rows did not record the administrator's checkbox. Preserve the
	// historical default for those rows; every newly written row is explicit.
	return true
}

func (d refundPendingAuditDetail) deductionTypeForOrder(o *dbent.PaymentOrder) string {
	if !d.deductionWasRequested() {
		return payment.DeductionTypeNone
	}
	switch d.DeductionType {
	case payment.DeductionTypeBalance, payment.DeductionTypeSubscription:
		return d.DeductionType
	}
	if o != nil && o.OrderType == payment.OrderTypeSubscription {
		return payment.DeductionTypeSubscription
	}
	return payment.DeductionTypeBalance
}

func (d refundPendingAuditDetail) pendingRefundAmount(o *dbent.PaymentOrder) float64 {
	if amount := validRefundAuditAmount(d.RefundAmount); amount > 0 {
		return amount
	}
	if o != nil {
		return validRefundAuditAmount(o.RefundAmount)
	}
	return 0
}

func (d refundPendingAuditDetail) deductionLineageID() string {
	if lineage := strings.TrimSpace(d.DeductionLineageID); lineage != "" {
		return lineage
	}
	if refundID := strings.TrimSpace(d.RefundID); refundID != "" {
		return refundID
	}
	return strings.TrimSpace(d.AttemptID)
}

func (d refundPendingAuditDetail) deductionNeedsRollbackRecovery() bool {
	// An applied-but-unrolled-back deduction is never safe to finalize without
	// recovery. Older audit rows may omit the amount fields; those rows are
	// handled as a manual-recovery failure rather than silently losing the
	// user's entitlement.
	if d.DeductionStateKnown && d.DeductionRequested != nil {
		return d.DeductionApplied
	}
	return !d.DeductionRollbackOK
}

func (s *PaymentService) recoverRefundDeduction(ctx context.Context, o *dbent.PaymentOrder, detail refundPendingAuditDetail, gatewayErr error) (bool, error) {
	if o == nil || !detail.deductionNeedsRollbackRecovery() {
		return true, nil
	}
	if detail.deductionLineageID() == "" {
		return false, errors.New("refund identifier missing for rollback recovery")
	}
	if s == nil || s.entClient == nil {
		return false, errors.New("payment database unavailable")
	}

	// Claim the order as failed in the same transaction as the compensation.
	// This closes the race where a concurrent successful query could finalize the
	// refund while another query was restoring the deduction.
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return false, fmt.Errorf("begin refund rollback recovery: %w", err)
	}
	rollback := func() {
		_ = tx.Rollback()
	}
	txCtx := dbent.NewTxContext(ctx, tx)
	orderQuery := tx.PaymentOrder.Query().Where(paymentorder.IDEQ(o.ID))
	if refundRecoverySupportsRowLock(s.entClient) {
		orderQuery = orderQuery.ForUpdate()
	}
	lockedOrder, err := orderQuery.Only(txCtx)
	if err != nil {
		rollback()
		return false, fmt.Errorf("lock refund order for rollback recovery: %w", err)
	}
	if lockedOrder.Status != o.Status {
		rollback()
		return false, infraerrors.Conflict("CONFLICT", "refund status changed")
	}
	failedAt := time.Now()
	claimed, err := tx.PaymentOrder.Update().
		Where(paymentorder.IDEQ(o.ID), paymentorder.StatusEQ(o.Status)).
		SetStatus(OrderStatusRefundFailed).
		SetFailedAt(failedAt).
		SetFailedReason(psErrMsg(gatewayErr)).
		Save(txCtx)
	if err != nil {
		rollback()
		return false, fmt.Errorf("claim failed refund for rollback recovery: %w", err)
	}
	if claimed == 0 {
		rollback()
		return false, infraerrors.Conflict("CONFLICT", "refund status changed")
	}
	recovered, err := refundRollbackWasRecoveredWithClient(txCtx, tx.Client(), o.ID, detail.deductionLineageID())
	if err != nil {
		rollback()
		return false, fmt.Errorf("check refund rollback recovery: %w", err)
	}
	if recovered {
		if _, err := tx.PaymentAuditLog.Create().
			SetOrderID(strconv.FormatInt(o.ID, 10)).
			SetAction("REFUND_FAILED").
			SetDetail(fmt.Sprintf(`{"detail":%q}`, psErrMsg(gatewayErr))).
			SetOperator("admin").
			Save(txCtx); err != nil {
			rollback()
			return false, fmt.Errorf("persist refund failure after recovered rollback: %w", err)
		}
		commitErr := tx.Commit()
		s.invalidateRefundSubscriptionCaches(o, detail.SubscriptionID, detail.SubDaysDeducted > 0)
		if commitErr != nil {
			return false, fmt.Errorf("commit refund rollback recovery check: %w", commitErr)
		}
		return true, nil
	}

	var rollbackErr error
	if balance := validRefundAuditAmount(detail.BalanceDeducted); balance > 0 {
		if s.userRepo == nil {
			rollbackErr = errors.New("user repository unavailable")
		} else {
			_, rollbackErr = s.userRepo.AdjustBalance(txCtx, o.UserID, balance)
		}
	} else if detail.SubDaysDeducted > 0 {
		if s.subscriptionSvc == nil || detail.SubscriptionID <= 0 {
			rollbackErr = errors.New("subscription rollback service unavailable")
		} else if detail.SubscriptionRevoked {
			_, rollbackErr = s.subscriptionSvc.restoreSubscription(txCtx, detail.SubscriptionID, true)
			if errors.Is(rollbackErr, ErrSubscriptionNotRevoked) {
				rollbackErr = nil
			}
		} else {
			_, rollbackErr = s.subscriptionSvc.extendSubscription(txCtx, detail.SubscriptionID, detail.SubDaysDeducted, true)
		}
	} else {
		rollback()
		return false, errors.New("refund rollback amount is missing")
	}
	if rollbackErr != nil {
		rollback()
		slog.Error("[CRITICAL] pending refund rollback recovery failed", "orderID", o.ID, "refundID", detail.RefundID, "error", rollbackErr)
		return false, rollbackErr
	}

	auditDetail, err := json.Marshal(map[string]any{
		"refundID":            detail.RefundID,
		"deductionLineageID":  detail.deductionLineageID(),
		"balanceRolledBack":   detail.BalanceDeducted,
		"subDaysRolledBack":   detail.SubDaysDeducted,
		"subscriptionID":      detail.SubscriptionID,
		"subscriptionRevoked": detail.SubscriptionRevoked,
	})
	if err != nil {
		rollback()
		return false, fmt.Errorf("marshal refund rollback recovery: %w", err)
	}
	if _, err := tx.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(o.ID, 10)).
		SetAction("REFUND_ROLLBACK_RECOVERED").
		SetDetail(string(auditDetail)).
		SetOperator("admin").
		Save(txCtx); err != nil {
		rollback()
		return false, fmt.Errorf("persist refund rollback recovery: %w", err)
	}
	if _, err := tx.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(o.ID, 10)).
		SetAction("REFUND_FAILED").
		SetDetail(fmt.Sprintf(`{"detail":%q}`, psErrMsg(gatewayErr))).
		SetOperator("admin").
		Save(txCtx); err != nil {
		rollback()
		return false, fmt.Errorf("persist refund failure after rollback recovery: %w", err)
	}
	commitErr := tx.Commit()
	s.invalidateRefundSubscriptionCaches(o, detail.SubscriptionID, detail.SubDaysDeducted > 0)
	if commitErr != nil {
		return false, fmt.Errorf("commit refund rollback recovery: %w", commitErr)
	}
	return true, nil
}

func refundRecoverySupportsRowLock(client *dbent.Client) bool {
	if client == nil || client.Driver() == nil {
		return false
	}
	switch client.Driver().Dialect() {
	case dialect.Postgres, dialect.MySQL:
		return true
	default:
		return false
	}
}

func (s *PaymentService) latestRefundPendingDetail(ctx context.Context, oid int64) refundPendingAuditDetail {
	return s.latestRefundAuditDetail(ctx, oid, "REFUND_PENDING")
}

func (s *PaymentService) latestRefundAttemptDetail(ctx context.Context, oid int64) refundPendingAuditDetail {
	return s.latestRefundAuditDetail(ctx, oid, "REFUND_ATTEMPT")
}

func (s *PaymentService) latestRefundAuditDetail(ctx context.Context, oid int64, action string) refundPendingAuditDetail {
	logEntry, err := s.entClient.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(oid, 10)), paymentauditlog.ActionEQ(action)).
		Order(paymentauditlog.ByCreatedAt(sql.OrderDesc())).
		First(ctx)
	if err != nil || logEntry == nil {
		detail := refundPendingAuditDetail{PreviousRefundAmount: s.latestRefundSuccessTotal(ctx, oid), DeductionRollbackOK: true}
		if action == "REFUND_ATTEMPT" {
			// A crash can occur after the entitlement mutation but before the
			// attempt audit is written. Do not assume the mutation was absent and
			// finalize/charge again; leave the order in explicit manual recovery.
			detail.DeductionRollbackOK = false
			detail.DeductionApplied = true
		}
		return detail
	}
	detail := refundPendingAuditDetail{DeductionRollbackOK: true}
	var fields map[string]json.RawMessage
	rawDetail := []byte(logEntry.Detail)
	if err := json.Unmarshal(rawDetail, &fields); err != nil || fields == nil {
		// A malformed audit record must never be treated as proof that a
		// deduction was rolled back. QueryAndFinalizeRefund will fail closed
		// before contacting the provider when the amount is unavailable.
		detail.DeductionRollbackOK = false
	} else if err := json.Unmarshal(rawDetail, &detail); err != nil {
		detail.DeductionRollbackOK = false
	} else {
		detail.DeductionStateKnown = refundAuditDeductionStateKnown(fields, detail)
		if detail.DeductionApplied {
			if _, recorded := fields["deductionRollbackOK"]; !recorded {
				// Legacy attempts persisted deductionApplied but not the rollback
				// outcome. Treat the outcome as unresolved until it is recovered.
				detail.DeductionRollbackOK = false
			}
		}
	}
	if action == "REFUND_ATTEMPT" && detail.DeductionApplied && detail.DeductionRollbackOK {
		// Legacy attempt rows recorded the pre-gateway deduction as already
		// rolled back. Treat that optimistic marker as unresolved; otherwise a
		// failed provider response could finalize without restoring the balance.
		detail.DeductionRollbackOK = false
	}
	detail.RefundID = strings.TrimSpace(detail.RefundID)
	detail.DeductionLineageID = detail.deductionLineageID()
	detail.RefundAmount = validRefundAuditAmount(detail.RefundAmount)
	detail.PreviousRefundAmount = validRefundAuditAmount(detail.PreviousRefundAmount)
	detail.TotalRefundAmount = validRefundAuditAmount(detail.TotalRefundAmount)
	detail.BalanceDeducted = validRefundAuditAmount(detail.BalanceDeducted)
	if detail.SubDaysDeducted < 0 {
		detail.SubDaysDeducted = 0
	}
	if detail.SubscriptionID < 0 {
		detail.SubscriptionID = 0
	}
	// Compatibility with pending records written before deductionApplied was
	// persisted explicitly. A failed rollback means the recorded deduction is
	// still in effect.
	if !detail.DeductionRollbackOK && (detail.BalanceDeducted > 0 || detail.SubDaysDeducted > 0) {
		detail.DeductionApplied = true
	}
	if detail.PreviousRefundAmount == 0 {
		detail.PreviousRefundAmount = s.latestRefundSuccessTotal(ctx, oid)
	}
	if detail.TotalRefundAmount == 0 && detail.RefundAmount > 0 {
		detail.TotalRefundAmount = detail.PreviousRefundAmount + detail.RefundAmount
	}
	return detail
}

func refundAuditDeductionStateKnown(fields map[string]json.RawMessage, detail refundPendingAuditDetail) bool {
	if raw, ok := fields["deductionStateKnown"]; ok {
		var known bool
		return json.Unmarshal(raw, &known) == nil && known
	}
	if _, ok := fields["deductionRequested"]; ok {
		return true
	}
	if _, ok := fields["deductionRollbackOK"]; ok {
		return true
	}
	if _, ok := fields["deductionApplied"]; ok {
		return true
	}
	return false
}

func (s *PaymentService) latestRefundSuccessTotal(ctx context.Context, oid int64) float64 {
	if s == nil || s.entClient == nil {
		return 0
	}
	logEntry, err := s.entClient.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(oid, 10)), paymentauditlog.ActionEQ("REFUND_SUCCESS")).
		Order(paymentauditlog.ByCreatedAt(sql.OrderDesc())).
		First(ctx)
	if err != nil || logEntry == nil {
		return 0
	}
	var detail struct {
		RefundAmount      float64 `json:"refundAmount"`
		TotalRefunded     float64 `json:"totalRefunded"`
		TotalRefundAmount float64 `json:"totalRefundAmount"`
	}
	if err := json.Unmarshal([]byte(logEntry.Detail), &detail); err != nil {
		return 0
	}
	if amount := validRefundAuditAmount(detail.TotalRefunded); amount > 0 {
		return amount
	}
	if amount := validRefundAuditAmount(detail.TotalRefundAmount); amount > 0 {
		return amount
	}
	return validRefundAuditAmount(detail.RefundAmount)
}

// refundSuccessfulWatermark returns only money proven to have reached a
// successful refund state. REFUND_FAILED may retain the request amount in the
// payment-order row (for example from a user-initiated request), so that field
// must not be treated as a successful tranche. A failed partial attempt carries
// its prior successful watermark in REFUND_ATTEMPT for recovery.
func (s *PaymentService) refundSuccessfulWatermark(ctx context.Context, o *dbent.PaymentOrder) float64 {
	if o == nil {
		return 0
	}
	watermark := s.latestRefundSuccessTotal(ctx, o.ID)
	if o.Status == OrderStatusPartiallyRefunded {
		if amount := validRefundAuditAmount(o.RefundAmount); amount > watermark {
			watermark = amount
		}
	}
	if o.Status == OrderStatusRefundFailed {
		attempt := s.latestRefundAttemptDetail(ctx, o.ID)
		if amount := validRefundAuditAmount(attempt.PreviousRefundAmount); amount > watermark {
			watermark = amount
		}
	}
	if watermark > o.Amount {
		return o.Amount
	}
	return watermark
}

func validRefundAuditAmount(amount float64) float64 {
	if amount <= 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0
	}
	return amount
}

func refundPreviousAmount(p *RefundPlan) float64 {
	if p == nil {
		return 0
	}
	if amount := validRefundAuditAmount(p.PreviousRefundAmount); amount > 0 {
		return amount
	}
	if p.Order != nil && p.Order.Status == OrderStatusPartiallyRefunded {
		return validRefundAuditAmount(p.Order.RefundAmount)
	}
	return 0
}

func refundFinalStatus(order *dbent.PaymentOrder, totalRefunded float64) string {
	if order != nil && order.Amount-totalRefunded > paymentAmountToleranceForCurrency(PaymentOrderCurrency(order)) {
		return OrderStatusPartiallyRefunded
	}
	return OrderStatusRefunded
}

// getRefundProvider creates a provider using the order's original instance config.
// Delegates to getOrderProvider which handles instance lookup and fallback.
func (s *PaymentService) getRefundProvider(ctx context.Context, o *dbent.PaymentOrder) (payment.Provider, error) {
	inst, err := s.getRefundOrderProviderInstance(ctx, o)
	if err != nil {
		return nil, err
	}
	if inst == nil {
		return nil, fmt.Errorf("refund provider instance is unavailable for order %d", o.ID)
	}
	return s.createProviderFromInstance(ctx, inst)
}

func (s *PaymentService) handleGwFail(ctx context.Context, p *RefundPlan, gErr error, terminalProviderFailure bool) (*RefundResult, error) {
	if s == nil || s.entClient == nil || p == nil || p.Order == nil {
		return nil, infraerrors.InternalServer("REFUND_FAILED", "refund state is unavailable").WithCause(gErr)
	}

	if p.DeductionAlreadyApplied {
		return s.recoverInheritedRefundDeductionAfterGatewayFailure(ctx, p, gErr)
	}

	if err := s.rollbackRefundAndRestore(ctx, p, gErr, terminalProviderFailure); err == nil {
		return &RefundResult{Success: false, Warning: "gateway failed: " + psErrMsg(gErr) + ", rolled back"}, nil
	} else {
		var commitUnknown *refundStateCommitOutcomeUnknownError
		if errors.As(err, &commitUnknown) {
			// The entitlement compensation, order transition, and recovery
			// marker share one transaction. An unknown COMMIT result is therefore
			// internally consistent, but must not trigger another mutation.
			return nil, infraerrors.InternalServer(
				"REFUND_ROLLBACK_COMMIT_UNKNOWN",
				"refund rollback result is uncertain; verify the order before retrying",
			).WithCause(err)
		}
		if persistErr := s.persistRefundFailureState(ctx, p, gErr, err); persistErr != nil {
			return nil, infraerrors.InternalServer(
				"REFUND_ROLLBACK_UNRESOLVED",
				"gateway refund failed and the deduction rollback state could not be persisted",
			).WithCause(fmt.Errorf("rollback: %v; persist failure: %w", err, persistErr))
		}
		return nil, infraerrors.InternalServer("REFUND_FAILED", psErrMsg(gErr)).WithCause(err)
	}
}

func (s *PaymentService) recoverInheritedRefundDeductionAfterGatewayFailure(
	ctx context.Context,
	p *RefundPlan,
	gErr error,
) (*RefundResult, error) {
	currentOrder, err := s.entClient.PaymentOrder.Get(ctx, p.OrderID)
	if err != nil {
		return nil, infraerrors.InternalServer("REFUND_ROLLBACK_UNRESOLVED", "refund order could not be reloaded").WithCause(err)
	}
	if currentOrder.Status != OrderStatusRefunding {
		return nil, infraerrors.Conflict("CONFLICT", "refund status changed")
	}
	detail := refundPendingAuditDetail{
		RefundID:             p.ProviderQueryID,
		AttemptID:            p.ProviderRefundID,
		DeductionLineageID:   p.deductionLineageID(),
		RefundAmount:         p.RefundAmount,
		PreviousRefundAmount: refundPreviousAmount(p),
		TotalRefundAmount:    refundPreviousAmount(p) + p.RefundAmount,
		DeductionRollbackOK:  false,
		DeductionApplied:     true,
		BalanceDeducted:      p.BalanceToDeduct,
		SubDaysDeducted:      p.SubDaysToDeduct,
		SubscriptionID:       p.SubscriptionID,
		SubscriptionRevoked:  p.SubscriptionRevoked,
	}
	detail, err = s.resolveLegacyRefundSubscriptionDetail(ctx, currentOrder, detail)
	if err != nil {
		return nil, infraerrors.Conflict("REFUND_ROLLBACK_UNRESOLVED", err.Error())
	}
	if validRefundAuditAmount(detail.BalanceDeducted) == 0 && detail.SubDaysDeducted <= 0 {
		return nil, infraerrors.Conflict("REFUND_ROLLBACK_UNRESOLVED", "refund deduction amount is unavailable; verify entitlement manually")
	}
	recovered, recoveryErr := s.recoverRefundDeduction(ctx, currentOrder, detail, gErr)
	if !recovered {
		s.writeAuditLog(ctx, p.OrderID, "REFUND_ROLLBACK_PENDING", "admin", map[string]any{
			"attemptID":          p.ProviderRefundID,
			"deductionLineageID": p.deductionLineageID(),
			"detail":             psErrMsg(recoveryErr),
		})
		return nil, infraerrors.InternalServer(
			"REFUND_ROLLBACK_UNRESOLVED",
			"gateway refund failed and the inherited deduction rollback is still pending",
		).WithCause(recoveryErr)
	}
	return &RefundResult{
		Success:         false,
		Warning:         "gateway failed: " + psErrMsg(gErr) + ", inherited deduction rolled back",
		BalanceDeducted: detail.BalanceDeducted,
		SubDaysDeducted: detail.SubDaysDeducted,
	}, nil
}

type refundStateCommitOutcomeUnknownError struct {
	operation string
	err       error
}

func (e *refundStateCommitOutcomeUnknownError) Error() string {
	return fmt.Sprintf("commit %s: %v", e.operation, e.err)
}

func (e *refundStateCommitOutcomeUnknownError) Unwrap() error {
	return e.err
}

// rollbackRefundAndRestore keeps entitlement compensation, order transition,
// and the durable recovery marker in one transaction. This prevents a worker
// from compensating the same deduction twice after a partial local failure.
func (s *PaymentService) rollbackRefundAndRestore(ctx context.Context, p *RefundPlan, gErr error, terminalProviderFailure bool) error {
	if s == nil || s.entClient == nil || p == nil || p.Order == nil {
		return errors.New("refund state is unavailable")
	}
	if p.DeductionAlreadyApplied {
		return errors.New("refund deduction belongs to an unresolved earlier attempt")
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin refund rollback: %w", err)
	}
	txCtx := dbent.NewTxContext(ctx, tx)
	if err := s.rollbackRefundWithError(txCtx, p); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("rollback refund entitlement: %w", err)
	}

	orderUpdate := tx.PaymentOrder.Update().
		Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusEQ(OrderStatusRefunding))
	if terminalProviderFailure {
		// A provider-declared terminal failure has consumed the current
		// idempotency key. Preserve REFUND_FAILED after compensating the local
		// entitlement so PrepareRefund generates a fresh key for the next try.
		orderUpdate = orderUpdate.
			SetStatus(OrderStatusRefundFailed).
			SetFailedAt(time.Now()).
			SetFailedReason(psErrMsg(gErr))
	} else {
		// Failures before a provider accepted the request (configuration,
		// snapshot validation, and similar local errors) may safely restore the
		// original order status and reuse the deterministic request key.
		orderUpdate = orderUpdate.
			SetStatus(refundRestoredStatus(p)).
			ClearFailedAt().
			ClearFailedReason()
	}
	updated, err := orderUpdate.Save(txCtx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("restore refund order status: %w", err)
	}
	if updated == 0 {
		_ = tx.Rollback()
		return infraerrors.Conflict("CONFLICT", "refund status changed")
	}

	recoveredDetail := map[string]any{
		"refundID":             p.ProviderQueryID,
		"attemptID":            p.ProviderRefundID,
		"deductionLineageID":   p.deductionLineageID(),
		"refundAmount":         p.RefundAmount,
		"previousRefundAmount": refundPreviousAmount(p),
		"balanceRolledBack":    p.BalanceToDeduct,
		"subDaysRolledBack":    p.SubDaysToDeduct,
		"subscriptionID":       p.SubscriptionID,
		"subscriptionRevoked":  p.SubscriptionRevoked,
	}
	if err := persistRefundAuditTx(txCtx, tx.Client(), p.OrderID, "REFUND_ROLLBACK_RECOVERED", recoveredDetail); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("persist refund rollback recovery: %w", err)
	}
	if err := persistRefundAuditTx(txCtx, tx.Client(), p.OrderID, "REFUND_GATEWAY_FAILED", map[string]any{
		"attemptID":          p.ProviderRefundID,
		"deductionLineageID": p.deductionLineageID(),
		"detail":             psErrMsg(gErr),
	}); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("persist refund gateway failure: %w", err)
	}
	commitErr := tx.Commit()
	s.invalidateRefundSubscriptionCaches(p.Order, p.SubscriptionID, p.SubDaysToDeduct > 0)
	if commitErr != nil {
		return &refundStateCommitOutcomeUnknownError{operation: "refund rollback", err: commitErr}
	}
	return nil
}

// persistRefundFailureState makes an unresolved rollback retriable only after
// its lineage marker and failed order status are durably committed together.
func (s *PaymentService) persistRefundFailureState(ctx context.Context, p *RefundPlan, gErr, rollbackErr error) error {
	if s == nil || s.entClient == nil || p == nil {
		return errors.New("refund state is unavailable")
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin refund failure state: %w", err)
	}
	txCtx := dbent.NewTxContext(ctx, tx)
	now := time.Now()
	updated, err := tx.PaymentOrder.Update().
		Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusEQ(OrderStatusRefunding)).
		SetStatus(OrderStatusRefundFailed).
		SetFailedAt(now).
		SetFailedReason(psErrMsg(gErr)).
		Save(txCtx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("mark refund failed: %w", err)
	}
	if updated == 0 {
		_ = tx.Rollback()
		return infraerrors.Conflict("CONFLICT", "refund status changed")
	}

	if rollbackErr != nil {
		if err := persistRefundAuditTx(txCtx, tx.Client(), p.OrderID, "REFUND_ROLLBACK_FAILED", refundRollbackFailureAuditDetail(p, gErr, rollbackErr)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("persist refund rollback failure: %w", err)
		}
	}
	if err := persistRefundAuditTx(txCtx, tx.Client(), p.OrderID, "REFUND_GATEWAY_FAILED", map[string]any{
		"attemptID":          p.ProviderRefundID,
		"deductionLineageID": p.deductionLineageID(),
		"detail":             psErrMsg(gErr),
	}); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("persist refund gateway failure: %w", err)
	}
	if err := persistRefundAuditTx(txCtx, tx.Client(), p.OrderID, "REFUND_FAILED", map[string]any{"detail": psErrMsg(gErr)}); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("persist refund failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return &refundStateCommitOutcomeUnknownError{operation: "refund failure state", err: err}
	}
	return nil
}

func persistRefundAuditTx(ctx context.Context, client *dbent.Client, oid int64, action string, detail map[string]any) error {
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(oid, 10)).
		SetAction(action).
		SetDetail(string(detailJSON)).
		SetOperator("admin").
		Save(ctx)
	return err
}

func (s *PaymentService) markRefundOk(ctx context.Context, p *RefundPlan) (*RefundResult, error) {
	previousRefundAmount := refundPreviousAmount(p)
	totalRefunded := previousRefundAmount + p.RefundAmount
	fs := refundFinalStatus(p.Order, totalRefunded)
	now := time.Now()
	updated, err := s.entClient.PaymentOrder.Update().
		Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusEQ(OrderStatusRefunding)).
		SetStatus(fs).SetRefundAmount(totalRefunded).SetRefundReason(p.Reason).SetRefundAt(now).SetForceRefund(p.Force).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("mark refund: %w", err)
	}
	if updated == 0 {
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}
	s.writeAuditLog(ctx, p.OrderID, "REFUND_SUCCESS", "admin", map[string]any{"refundID": p.ProviderQueryID, "attemptID": p.ProviderRefundID, "deductionLineageID": p.deductionLineageID(), "refundAmount": p.RefundAmount, "previousRefundAmount": previousRefundAmount, "totalRefunded": totalRefunded, "reason": p.Reason, "balanceDeducted": p.BalanceToDeduct, "subscriptionDaysDeducted": p.SubDaysToDeduct, "force": p.Force})
	return &RefundResult{Success: true, BalanceDeducted: p.BalanceToDeduct, SubDaysDeducted: p.SubDaysToDeduct}, nil
}

func (s *PaymentService) markRefundOkTx(ctx context.Context, client *dbent.Client, p *RefundPlan) (*RefundResult, error) {
	previousRefundAmount := refundPreviousAmount(p)
	totalRefunded := previousRefundAmount + p.RefundAmount
	fs := refundFinalStatus(p.Order, totalRefunded)
	now := time.Now()
	_, err := client.PaymentOrder.UpdateOneID(p.OrderID).SetStatus(fs).SetRefundAmount(totalRefunded).SetRefundReason(p.Reason).SetRefundAt(now).SetForceRefund(p.Force).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("mark refund: %w", err)
	}
	detail, err := json.Marshal(map[string]any{"refundID": p.ProviderQueryID, "attemptID": p.ProviderRefundID, "deductionLineageID": p.deductionLineageID(), "refundAmount": p.RefundAmount, "previousRefundAmount": previousRefundAmount, "totalRefunded": totalRefunded, "reason": p.Reason, "balanceDeducted": p.BalanceToDeduct, "subscriptionDaysDeducted": p.SubDaysToDeduct, "force": p.Force})
	if err != nil {
		return nil, fmt.Errorf("marshal refund audit: %w", err)
	}
	if _, err := client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(p.OrderID, 10)).
		SetAction("REFUND_SUCCESS").
		SetDetail(string(detail)).
		SetOperator("admin").
		Save(ctx); err != nil {
		return nil, fmt.Errorf("write refund audit: %w", err)
	}
	return &RefundResult{Success: true, BalanceDeducted: p.BalanceToDeduct, SubDaysDeducted: p.SubDaysToDeduct}, nil
}

func (s *PaymentService) markRefundPending(ctx context.Context, p *RefundPlan, resp *payment.RefundResponse) (*RefundResult, error) {
	previousRefundAmount := refundPreviousAmount(p)
	totalRefundAmount := previousRefundAmount + p.RefundAmount
	balanceDeducted := p.BalanceToDeduct
	subDaysDeducted := p.SubDaysToDeduct
	// A retry may inherit a prior deduction whose rollback failed. That
	// deduction is still applied, so attempting another rollback here could
	// credit the account twice when the provider remains pending.
	if p.DeductionAlreadyApplied {
		return s.persistRefundPendingState(ctx, p, resp, previousRefundAmount, totalRefundAmount,
			balanceDeducted, subDaysDeducted, 0, 0, false)
	}

	// The entitlement rollback and the pending state must commit together. If
	// the order update fails after a successful rollback, rolling the transaction
	// back keeps the original deduction durable and the REFUND_ATTEMPT marker
	// remains an accurate recovery record.
	tx, err := s.entClient.Tx(ctx)
	if err == nil {
		txCtx := dbent.NewTxContext(ctx, tx)
		rollbackErr := s.rollbackRefundWithError(txCtx, p)
		rollbackOK := rollbackErr == nil
		if rollbackErr == nil {
			result, persistErr := s.persistRefundPendingTx(txCtx, tx.Client(), p, resp, previousRefundAmount, totalRefundAmount,
				balanceDeducted, subDaysDeducted, balanceDeducted, subDaysDeducted, rollbackOK)
			if persistErr == nil {
				if commitErr := tx.Commit(); commitErr == nil {
					s.invalidateRefundSubscriptionCaches(p.Order, p.SubscriptionID, p.SubDaysToDeduct > 0)
					if rollbackOK {
						p.BalanceToDeduct = 0
						p.SubDaysToDeduct = 0
						p.SubscriptionRevoked = false
					}
					return result, nil
				} else {
					s.invalidateRefundSubscriptionCaches(p.Order, p.SubscriptionID, p.SubDaysToDeduct > 0)
					return nil, fmt.Errorf("commit refund pending: %w", commitErr)
				}
			}
			_ = tx.Rollback()
			return nil, persistErr
		}
		_ = tx.Rollback()
		// The transaction may be aborted by the failed rollback statement. Keep
		// the deduction applied and persist a pending record in a fresh transaction.
		s.writeAuditLog(ctx, p.OrderID, "REFUND_ROLLBACK_FAILED", "admin", refundRollbackFailureAuditDetail(p, nil, rollbackErr))
		return s.persistRefundPendingState(ctx, p, resp, previousRefundAmount, totalRefundAmount,
			balanceDeducted, subDaysDeducted, 0, 0, false)
	}

	// A transaction could not be started, so do not mutate the entitlement. The
	// existing REFUND_ATTEMPT remains the durable marker for later recovery.
	return s.persistRefundPendingState(ctx, p, resp, previousRefundAmount, totalRefundAmount,
		balanceDeducted, subDaysDeducted, 0, 0, false)
}

func (s *PaymentService) persistRefundPendingState(
	ctx context.Context,
	p *RefundPlan,
	resp *payment.RefundResponse,
	previousRefundAmount, totalRefundAmount, balanceDeducted float64,
	subDaysDeducted int,
	balanceRolledBack float64,
	subDaysRolledBack int,
	rollbackOK bool,
) (*RefundResult, error) {
	if s == nil || s.entClient == nil {
		return nil, errors.New("payment database unavailable")
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin refund pending: %w", err)
	}
	txCtx := dbent.NewTxContext(ctx, tx)
	result, err := s.persistRefundPendingTx(txCtx, tx.Client(), p, resp, previousRefundAmount, totalRefundAmount,
		balanceDeducted, subDaysDeducted, balanceRolledBack, subDaysRolledBack, rollbackOK)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit refund pending: %w", err)
	}
	return result, nil
}

func (s *PaymentService) persistRefundPendingTx(
	ctx context.Context,
	client *dbent.Client,
	p *RefundPlan,
	resp *payment.RefundResponse,
	previousRefundAmount, totalRefundAmount, balanceDeducted float64,
	subDaysDeducted int,
	balanceRolledBack float64,
	subDaysRolledBack int,
	rollbackOK bool,
) (*RefundResult, error) {
	updated, err := client.PaymentOrder.Update().
		Where(paymentorder.IDEQ(p.OrderID), paymentorder.StatusEQ(OrderStatusRefunding)).
		SetStatus(OrderStatusRefundPending).
		SetRefundAmount(p.RefundAmount).
		SetRefundReason(p.Reason).
		ClearRefundAt().
		SetForceRefund(p.Force).
		ClearFailedAt().
		ClearFailedReason().
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("mark refund pending: %w", err)
	}
	if updated == 0 {
		return nil, infraerrors.Conflict("CONFLICT", "order status changed")
	}

	deductionRequested := refundDeductionWasRequested(p)
	detail := map[string]any{
		"refundID":             refundPendingResponseID(resp, p.ProviderQueryID),
		"attemptID":            strings.TrimSpace(p.ProviderRefundID),
		"deductionLineageID":   p.deductionLineageID(),
		"refundAmount":         p.RefundAmount,
		"previousRefundAmount": previousRefundAmount,
		"totalRefundAmount":    totalRefundAmount,
		"reason":               p.Reason,
		"force":                p.Force,
		"balanceDeducted":      balanceDeducted,
		"subDaysDeducted":      subDaysDeducted,
		"balanceRolledBack":    balanceRolledBack,
		"subDaysRolledBack":    subDaysRolledBack,
		"deductionRollbackOK":  rollbackOK,
		"deductionApplied":     !rollbackOK && (balanceDeducted > 0 || subDaysDeducted > 0),
		"deductionStateKnown":  true,
		"deductionRequested":   deductionRequested,
		"deductionType":        refundPlanDeductionType(p, deductionRequested),
		"subscriptionID":       p.SubscriptionID,
		"subscriptionRevoked":  p.SubscriptionRevoked && !rollbackOK,
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return nil, fmt.Errorf("marshal refund pending audit: %w", err)
	}
	if _, err := client.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(p.OrderID, 10)).
		SetAction("REFUND_PENDING").
		SetDetail(string(detailJSON)).
		SetOperator("admin").
		Save(ctx); err != nil {
		return nil, fmt.Errorf("write refund pending audit: %w", err)
	}

	warning := "gateway refund is pending confirmation"
	if !rollbackOK {
		warning += "; refund deduction rollback failed"
	}
	return &RefundResult{Success: false, Warning: warning}, nil
}

func refundPendingResponseID(resp *payment.RefundResponse, fallback string) string {
	if refundID := refundResponseID(resp); refundID != "" {
		return refundID
	}
	return strings.TrimSpace(fallback)
}

func refundResponseID(resp *payment.RefundResponse) string {
	if resp == nil {
		return ""
	}
	return strings.TrimSpace(resp.RefundID)
}

func refundResponseStatus(resp *payment.RefundResponse) string {
	if resp == nil {
		return ""
	}
	return strings.TrimSpace(resp.Status)
}

func refundProviderQueryID(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	// Our synthetic attempt IDs are exactly rf_ plus 24 lowercase hex
	// characters. Do not reject arbitrary provider IDs that merely happen to
	// use an rf_ prefix (and keep legacy/test IDs such as rf_test queryable).
	if len(value) == len("rf_")+24 && strings.HasPrefix(value, "rf_") {
		for _, ch := range value[len("rf_"):] {
			if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
				return value
			}
		}
		return ""
	}
	return value
}

func (s *PaymentService) RollbackRefund(ctx context.Context, p *RefundPlan, gErr error) bool {
	if err := s.rollbackRefundAndRestore(ctx, p, gErr, false); err != nil {
		orderID := int64(0)
		if p != nil {
			orderID = p.OrderID
		}
		slog.Error("[CRITICAL] rollback failed", "orderID", orderID, "error", err)
		var commitUnknown *refundStateCommitOutcomeUnknownError
		if !errors.As(err, &commitUnknown) {
			if persistErr := s.persistRefundFailureState(ctx, p, gErr, err); persistErr != nil {
				slog.Error("[CRITICAL] persist refund rollback failure failed", "orderID", orderID, "error", persistErr)
			}
		}
		return false
	}
	return true
}

// rollbackRefundWithError applies the entitlement compensation using the
// transaction carried by ctx. The caller owns the surrounding state transition
// and durable recovery marker and must commit all three changes atomically.
func (s *PaymentService) rollbackRefundWithError(ctx context.Context, p *RefundPlan) error {
	if p == nil || p.DeductionAlreadyApplied {
		return nil
	}
	if s == nil || p.Order == nil {
		return errors.New("refund order unavailable")
	}
	if p.DeductionType == payment.DeductionTypeBalance && p.BalanceToDeduct > 0 && s.userRepo == nil {
		return errors.New("user repository unavailable")
	}
	if p.DeductionType == payment.DeductionTypeBalance && p.BalanceToDeduct > 0 {
		if _, err := s.userRepo.AdjustBalance(ctx, p.Order.UserID, p.BalanceToDeduct); err != nil {
			return err
		}
	}
	if p.DeductionType == payment.DeductionTypeSubscription && p.SubDaysToDeduct > 0 && p.SubscriptionID > 0 {
		if s.subscriptionSvc == nil {
			return errors.New("subscription rollback service unavailable")
		}
		if p.SubscriptionRevoked {
			if _, err := s.subscriptionSvc.restoreSubscription(ctx, p.SubscriptionID, true); err != nil && !errors.Is(err, ErrSubscriptionNotRevoked) {
				return err
			}
		} else if _, err := s.subscriptionSvc.extendSubscription(ctx, p.SubscriptionID, p.SubDaysToDeduct, true); err != nil {
			return err
		}
	}
	return nil
}

func refundRollbackFailureAuditDetail(p *RefundPlan, gatewayErr, rollbackErr error) map[string]any {
	detail := map[string]any{
		"gatewayError":     psErrMsg(gatewayErr),
		"rollbackError":    psErrMsg(rollbackErr),
		"deductionApplied": true,
	}
	if p == nil {
		return detail
	}
	detail["refundID"] = p.ProviderQueryID
	detail["attemptID"] = p.ProviderRefundID
	detail["deductionLineageID"] = p.deductionLineageID()
	detail["refundAmount"] = p.RefundAmount
	detail["previousRefundAmount"] = refundPreviousAmount(p)
	detail["balanceDeducted"] = p.BalanceToDeduct
	detail["subDaysDeducted"] = p.SubDaysToDeduct
	detail["subscriptionID"] = p.SubscriptionID
	detail["subscriptionRevoked"] = p.SubscriptionRevoked
	return detail
}

// latestUnresolvedRefundRollback returns a rollback failure for the current
// refund tranche that has not been recovered or finalized by that same tranche.
// A retry may use a new provider idempotency key, so the provider ID is only
// the first association key. If it does not match, the refund amount and
// previous-refund watermark are used as a conservative local lineage fallback.
// Ambiguous or unmatchable failures fail closed instead of risking a second
// entitlement deduction.
func (s *PaymentService) latestUnresolvedRefundRollback(ctx context.Context, p *RefundPlan) (refundPendingAuditDetail, bool, error) {
	if s == nil || s.entClient == nil || p == nil {
		return refundPendingAuditDetail{}, false, nil
	}
	failures, err := s.entClient.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)), paymentauditlog.ActionEQ("REFUND_ROLLBACK_FAILED")).
		Order(paymentauditlog.ByCreatedAt(sql.OrderDesc())).
		All(ctx)
	if err != nil {
		return refundPendingAuditDetail{}, false, fmt.Errorf("list unresolved refund rollbacks: %w", err)
	}
	if len(failures) == 0 {
		return refundPendingAuditDetail{}, false, nil
	}
	resolved, err := s.entClient.PaymentAuditLog.Query().
		Where(
			paymentauditlog.OrderIDEQ(strconv.FormatInt(p.OrderID, 10)),
			paymentauditlog.ActionIn("REFUND_ROLLBACK_RECOVERED", "REFUND_SUCCESS"),
		).
		Order(paymentauditlog.ByCreatedAt(sql.OrderAsc())).
		All(ctx)
	if err != nil {
		return refundPendingAuditDetail{}, false, fmt.Errorf("list refund rollback resolutions: %w", err)
	}
	resolvedAt := make(map[string]time.Time)
	for _, entry := range resolved {
		if lineage := refundAuditLineageID(entry.Detail); lineage != "" {
			if previous, ok := resolvedAt[lineage]; !ok || entry.CreatedAt.After(previous) {
				resolvedAt[lineage] = entry.CreatedAt
			}
		}
	}

	if p.ProviderRefundID == "" && p.deductionLineageID() == "" {
		return refundPendingAuditDetail{}, false, nil
	}
	type unresolvedRollback struct {
		detail  refundPendingAuditDetail
		created time.Time
	}
	candidates := make([]unresolvedRollback, 0, len(failures))
	for _, failed := range failures {
		detail := refundPendingAuditDetail{}
		if err := json.Unmarshal([]byte(failed.Detail), &detail); err != nil {
			return refundPendingAuditDetail{}, false, fmt.Errorf("decode unresolved refund rollback %d: %w", failed.ID, err)
		}
		detail.RefundID = strings.TrimSpace(detail.RefundID)
		detail.DeductionLineageID = detail.deductionLineageID()
		if detail.DeductionLineageID == "" {
			// Legacy rollback-failure rows predate attempt/refund identifiers. The
			// audit row itself is immutable, so its primary key is a stable lineage
			// that later RECOVERED/SUCCESS markers can resolve exactly once.
			detail.DeductionLineageID = legacyRefundRollbackLineage(failed.ID)
		}
		lineage := detail.deductionLineageID()
		if lineage != "" {
			if resolvedAt, ok := resolvedAt[lineage]; ok && !resolvedAt.Before(failed.CreatedAt) {
				continue
			}
		}
		detail.BalanceDeducted = validRefundAuditAmount(detail.BalanceDeducted)
		if detail.SubDaysDeducted < 0 {
			detail.SubDaysDeducted = 0
		}
		if detail.BalanceDeducted == 0 && detail.SubDaysDeducted == 0 {
			return refundPendingAuditDetail{}, false, errors.New("unresolved refund rollback is missing its deduction amount")
		}
		if detail.SubscriptionID <= 0 && detail.SubDaysDeducted > 0 && p.SubscriptionID > 0 {
			detail.SubscriptionID = p.SubscriptionID
		}
		detail.DeductionApplied = true
		detail.DeductionRollbackOK = false
		candidates = append(candidates, unresolvedRollback{detail: detail, created: failed.CreatedAt})
	}
	if len(candidates) == 0 {
		return refundPendingAuditDetail{}, false, nil
	}
	selectCandidate := func(matches []unresolvedRollback) (refundPendingAuditDetail, bool, error) {
		switch len(matches) {
		case 0:
			return refundPendingAuditDetail{}, false, nil
		case 1:
			return matches[0].detail, true, nil
		default:
			return refundPendingAuditDetail{}, false, errors.New("multiple unresolved refund deductions require manual recovery")
		}
	}
	exact := make([]unresolvedRollback, 0, len(candidates))
	for _, candidate := range candidates {
		lineage := candidate.detail.deductionLineageID()
		if (candidate.detail.RefundID != "" && candidate.detail.RefundID == p.ProviderRefundID) || (lineage != "" && lineage == p.deductionLineageID()) {
			exact = append(exact, candidate)
		}
	}
	if detail, ok, err := selectCandidate(exact); ok || err != nil {
		return detail, ok, err
	}
	fallback := make([]unresolvedRollback, 0, len(candidates))
	for _, candidate := range candidates {
		if refundAuditAmountsMatch(candidate.detail, p) {
			fallback = append(fallback, candidate)
		}
	}
	if detail, ok, err := selectCandidate(fallback); ok || err != nil {
		return detail, ok, err
	}
	return refundPendingAuditDetail{}, false, errors.New("an unresolved refund deduction cannot be associated with this retry")
}

func legacyRefundRollbackLineage(auditID int64) string {
	return "legacy-refund-rollback:" + strconv.FormatInt(auditID, 10)
}

func refundAuditAmountsMatch(detail refundPendingAuditDetail, p *RefundPlan) bool {
	if p == nil {
		return false
	}
	currency := "CNY"
	if p.Order != nil {
		currency = PaymentOrderCurrency(p.Order)
	}
	tolerance := paymentAmountToleranceForCurrency(currency)
	if validRefundAuditAmount(detail.RefundAmount) > 0 && math.Abs(validRefundAuditAmount(detail.RefundAmount)-validRefundAuditAmount(p.RefundAmount)) > tolerance {
		return false
	}
	if validRefundAuditAmount(detail.PreviousRefundAmount) > 0 && math.Abs(validRefundAuditAmount(detail.PreviousRefundAmount)-validRefundAuditAmount(p.PreviousRefundAmount)) > tolerance {
		return false
	}
	if validRefundAuditAmount(detail.BalanceDeducted) > 0 && validRefundAuditAmount(p.BalanceToDeduct) > 0 && math.Abs(validRefundAuditAmount(detail.BalanceDeducted)-validRefundAuditAmount(p.BalanceToDeduct)) > tolerance {
		return false
	}
	if detail.SubDaysDeducted > 0 && p.SubDaysToDeduct > 0 && detail.SubDaysDeducted != p.SubDaysToDeduct {
		return false
	}
	if detail.SubscriptionID > 0 && p.SubscriptionID > 0 && detail.SubscriptionID != p.SubscriptionID {
		return false
	}
	return true
}

func refundAuditRefundID(raw string) string {
	var detail struct {
		RefundID string `json:"refundID"`
	}
	if err := json.Unmarshal([]byte(raw), &detail); err != nil {
		return ""
	}
	return strings.TrimSpace(detail.RefundID)
}

func refundAuditLineageID(raw string) string {
	var detail struct {
		RefundID           string `json:"refundID"`
		AttemptID          string `json:"attemptID"`
		DeductionLineageID string `json:"deductionLineageID"`
	}
	if err := json.Unmarshal([]byte(raw), &detail); err != nil {
		return ""
	}
	if lineage := strings.TrimSpace(detail.DeductionLineageID); lineage != "" {
		return lineage
	}
	if refundID := strings.TrimSpace(detail.RefundID); refundID != "" {
		return refundID
	}
	return strings.TrimSpace(detail.AttemptID)
}

func refundRollbackWasRecoveredWithClient(ctx context.Context, client *dbent.Client, oid int64, lineageID string) (bool, error) {
	lineageID = strings.TrimSpace(lineageID)
	if client == nil || lineageID == "" {
		return false, nil
	}
	entries, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(strconv.FormatInt(oid, 10)), paymentauditlog.ActionEQ("REFUND_ROLLBACK_RECOVERED")).
		All(ctx)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		// New rows carry the stable deduction lineage. The fallback keeps
		// recovery idempotent for legacy rows that only persisted refundID.
		if refundAuditLineageID(entry.Detail) == lineageID || refundAuditRefundID(entry.Detail) == lineageID {
			return true, nil
		}
	}
	return false, nil
}

func (s *PaymentService) restoreStatus(ctx context.Context, p *RefundPlan) {
	if s == nil || s.entClient == nil || p == nil {
		return
	}
	_, _ = s.entClient.PaymentOrder.UpdateOneID(p.OrderID).SetStatus(refundRestoredStatus(p)).Save(ctx)
}

func refundRestoredStatus(p *RefundPlan) string {
	rs := OrderStatusCompleted
	if p == nil || p.Order == nil {
		return rs
	}
	switch p.Order.Status {
	case OrderStatusRefundRequested:
		rs = OrderStatusRefundRequested
	case OrderStatusRefundPending:
		rs = OrderStatusRefundPending
	case OrderStatusRefundFailed:
		rs = OrderStatusRefundFailed
	case OrderStatusPartiallyRefunded:
		rs = OrderStatusPartiallyRefunded
	case OrderStatusRefunding:
		// A failed in-flight attempt must remain actionable for manual retry;
		// never silently restore it to a completed order.
		rs = OrderStatusRefundFailed
	}
	return rs
}
