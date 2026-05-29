import type { SubscriptionType } from '@/types'

export const SUBSCRIPTION_MONTHLY_TYPE: SubscriptionType = 'subscription'
export const SUBSCRIPTION_WEEKLY_TYPE: SubscriptionType = 'subscription_weekly'
export const SUBSCRIPTION_DAILY_TYPE: SubscriptionType = 'subscription_daily'

export function isSubscriptionType(value?: string | null): value is SubscriptionType {
  return (
    value === SUBSCRIPTION_MONTHLY_TYPE ||
    value === SUBSCRIPTION_WEEKLY_TYPE ||
    value === SUBSCRIPTION_DAILY_TYPE
  )
}

export function allowsDailyLimit(value?: string | null): boolean {
  return isSubscriptionType(value)
}

export function allowsWeeklyLimit(value?: string | null): boolean {
  return value === SUBSCRIPTION_MONTHLY_TYPE || value === SUBSCRIPTION_WEEKLY_TYPE
}

export function allowsMonthlyLimit(value?: string | null): boolean {
  return value === SUBSCRIPTION_MONTHLY_TYPE
}

export function subscriptionTypeLabelKey(value?: string | null): string {
  switch (value) {
    case SUBSCRIPTION_MONTHLY_TYPE:
      return 'admin.groups.subscription.subscriptionMonthly'
    case SUBSCRIPTION_WEEKLY_TYPE:
      return 'admin.groups.subscription.subscriptionWeekly'
    case SUBSCRIPTION_DAILY_TYPE:
      return 'admin.groups.subscription.subscriptionDaily'
    default:
      return 'admin.groups.subscription.standard'
  }
}
