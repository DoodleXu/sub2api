import type { Account } from '@/types'

export const OPENAI_CONTINUE_SCHEDULING_AFTER_LIMIT_KEY = 'openai_continue_scheduling_after_limit'

const isFutureTime = (value: string | null | undefined, now: number): boolean => {
  if (!value) return false
  const timestamp = new Date(value).getTime()
  return Number.isFinite(timestamp) && timestamp > now
}

export const isOpenAIContinueSchedulingAfterLimitEnabled = (account: Account): boolean => {
  return account.platform === 'openai' && account.extra?.[OPENAI_CONTINUE_SCHEDULING_AFTER_LIMIT_KEY] === true
}

const isOpenAI429TempBlock = (account: Account): boolean => {
  if (!account.temp_unschedulable_reason) return false
  try {
    const reason = JSON.parse(account.temp_unschedulable_reason) as { status_code?: unknown }
    return Number(reason.status_code) === 429
  } catch {
    return false
  }
}

export const getAccountLimitSchedulingState = (account: Account, now = Date.now()) => {
  const continueAfterLimit = isOpenAIContinueSchedulingAfterLimitEnabled(account)
  const rawRateLimited = isFutureTime(account.rate_limit_reset_at, now)
  const rawTempUnschedulable = isFutureTime(account.temp_unschedulable_until, now)
  const bypassedTemp429 = continueAfterLimit && rawTempUnschedulable && isOpenAI429TempBlock(account)
  const tempUnschedulable = rawTempUnschedulable && !bypassedTemp429

  return {
    overclocking: continueAfterLimit && !tempUnschedulable,
    rateLimited: rawRateLimited && !continueAfterLimit,
    tempUnschedulable
  }
}
