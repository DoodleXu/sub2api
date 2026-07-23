import type { SubscriptionPlan } from '@/types/payment'
import { formatValidityPeriod } from '@/utils/validityUnit'

type TranslateFn = (key: string) => string

// 用户侧套餐有效期统一复用 fork 的 day/week/month/year 归一化逻辑，
// 保证单复数管理端值与后端 psComputeValidityDays 的计费语义一致。
export function planValiditySuffix(
  plan: Pick<SubscriptionPlan, 'validity_days' | 'validity_unit'>,
  t: TranslateFn,
): string {
  return formatValidityPeriod(plan.validity_days, plan.validity_unit, t)
}
