import type { Account } from '@/types'


export const isAccountArchived = (account: Pick<Account, 'archived_at' | 'parent_archived_at'>): boolean => {
  return Boolean(account.archived_at || account.parent_archived_at)
}

const isFutureTime = (value: string | null | undefined, now: number): boolean => {
  if (!value) return false
  const timestamp = new Date(value).getTime()
  return Number.isFinite(timestamp) && timestamp > now
}

export const getAccountLimitSchedulingState = (account: Account, now = Date.now()) => ({
  rateLimited: isFutureTime(account.rate_limit_reset_at, now),
  tempUnschedulable: isFutureTime(account.temp_unschedulable_until, now)
})
