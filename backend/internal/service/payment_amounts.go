package service

import (
	"math"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/shopspring/decimal"
)

const defaultBalanceRechargeMultiplier = 1.0

func normalizeBalanceRechargeMultiplier(multiplier float64) float64 {
	if math.IsNaN(multiplier) || math.IsInf(multiplier, 0) || multiplier <= 0 {
		return defaultBalanceRechargeMultiplier
	}
	return multiplier
}

func calculateCreditedBalance(paymentAmount, multiplier float64) float64 {
	return decimal.NewFromFloat(paymentAmount).
		Mul(decimal.NewFromFloat(normalizeBalanceRechargeMultiplier(multiplier))).
		Round(2).
		InexactFloat64()
}

func calculateGatewayRefundAmount(orderAmount, payAmount, refundAmount float64, currency string) float64 {
	if orderAmount <= 0 || payAmount <= 0 || refundAmount <= 0 {
		return 0
	}
	fractionDigits := int32(payment.CurrencyMaxFractionDigits(currency))
	if math.Abs(refundAmount-orderAmount) <= paymentAmountToleranceForCurrency(currency) {
		return decimal.NewFromFloat(payAmount).Round(fractionDigits).InexactFloat64()
	}
	return decimal.NewFromFloat(payAmount).
		Mul(decimal.NewFromFloat(refundAmount)).
		Div(decimal.NewFromFloat(orderAmount)).
		Round(fractionDigits).
		InexactFloat64()
}

type paymentFeeConfig struct {
	Rate float64
	Min  float64
}

func paymentFeeConfigForSelection(globalRate float64, sel *payment.InstanceSelection) paymentFeeConfig {
	if sel == nil || strings.TrimSpace(sel.ProviderKey) != payment.TypeStripe || !stripeFeeConfigOverridesGlobal(sel.Config) {
		return paymentFeeConfig{Rate: globalRate}
	}
	return paymentFeeConfig{
		Rate: parseProviderFeeFloat(sel.Config[payment.ConfigKeyFeeRate]),
		Min:  parseProviderFeeFloat(sel.Config[payment.ConfigKeyFeeMin]),
	}
}

func stripeFeeConfigOverridesGlobal(config map[string]string) bool {
	return stripeFeeConfigHasCompleteOverride(config)
}

func stripeFeeConfigHasAnyOverride(config map[string]string) bool {
	if config == nil {
		return false
	}
	return strings.TrimSpace(config[payment.ConfigKeyFeeRate]) != "" ||
		strings.TrimSpace(config[payment.ConfigKeyFeeMin]) != ""
}

func stripeFeeConfigHasCompleteOverride(config map[string]string) bool {
	if config == nil {
		return false
	}
	return strings.TrimSpace(config[payment.ConfigKeyFeeRate]) != "" &&
		strings.TrimSpace(config[payment.ConfigKeyFeeMin]) != ""
}

func parseProviderFeeFloat(raw string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}
