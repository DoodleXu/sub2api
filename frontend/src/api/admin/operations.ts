import { apiClient } from '../client'
import type { DailyCheckinAdminStats, SystemSettings, UpdateSettingsRequest } from './settings'

export type OperationsAdminStats = Omit<DailyCheckinAdminStats, 'meta'> & { meta: OperationsDataMeta }

export type DailyCheckinSettingsUpdateRequest = Pick<
  UpdateSettingsRequest,
  | 'daily_checkin_enabled'
  | 'daily_checkin_required_usage_usd'
  | 'daily_checkin_usage_scope'
  | 'daily_checkin_reward_min_usd'
  | 'daily_checkin_reward_max_usd'
  | 'daily_checkin_daily_budget_usd'
  | 'daily_checkin_monthly_budget_usd'
  | 'daily_checkin_user_monthly_limit_usd'
  | 'daily_checkin_budget_fallback_reward_usd'
  | 'daily_checkin_budget_fallback_message'
  | 'daily_checkin_reward_tiers'
  | 'daily_checkin_streak_multiplier_enabled'
  | 'daily_checkin_streak_multiplier_scope'
  | 'daily_checkin_streak_multipliers'
  | 'daily_checkin_crit_enabled'
  | 'daily_checkin_crit_probability_percent'
  | 'daily_checkin_crit_multiplier'
  | 'daily_checkin_crit_max_reward_usd'
>

export interface DailyCheckinRewardMetadata {
  base_reward_amount: number
  reward_tier?: {
    min_usd: number
    max_usd: number
    probability_percent: number
  }
  streak_days: number
  streak_multiplier: number
  crit_eligible: boolean
  crit_hit: boolean
  crit_multiplier: number
  pre_crit_reward_amount: number
  final_reward_amount: number
  required_usage_usd?: number
  usage_scope?: 'actual_cost' | 'balance_only'
  rule_effective_at?: string
  budget_fallback?: boolean
  budget_fallback_message?: string
}

export interface DailyCheckinAdminRecord {
  id: number
  user_id: number
  username: string
  email: string
  date: string
  reward_amount: number
  qualified_usage_usd: number
  reward_metadata?: DailyCheckinRewardMetadata
  created_at: string
}

export interface DailyCheckinRecordListResponse {
  items: DailyCheckinAdminRecord[]
  total: number
  page: number
  page_size: number
}

export interface DailyCheckinRecordQuery {
  page?: number
  page_size?: number
  date_from?: string
  date_to?: string
  user?: string
  reward_min?: number
  reward_max?: number
  crit_hit?: boolean | ''
  streak_days?: number
}

export interface OperationsDateRangeQuery {
  start_date?: string
  end_date?: string
  timezone?: string
}

export interface OperationsOverviewPoint {
  date: string
  dau: number
  new_users: number
  request_users: number
  requests: number
  actual_cost: number
}

export interface OperationsOverviewSummary {
  dau: number
  last_day_dau: number
  new_users: number
  request_users: number
  period_request_users: number
  requests: number
  actual_cost: number
}

export interface OperationsDataMeta {
  timezone: string
  as_of: string
  requested_start?: string
  requested_end?: string
  coverage_start?: string
  coverage_end?: string
  data_quality: 'complete' | 'partial' | 'empty' | 'unknown'
  source: string
  stale: boolean
  warnings?: string[]
}

export interface OperationsOverviewResponse {
  summary: OperationsOverviewSummary
  points: OperationsOverviewPoint[]
  meta: OperationsDataMeta
}

export interface OperationsRetentionSummary {
  cohort_users: number
  d1_eligible_users: number
  d7_eligible_users: number
  d30_eligible_users: number
  d1_users: number
  d7_users: number
  d30_users: number
  d1_rate: number
  d7_rate: number
  d30_rate: number
  average_active_days: number
}

export interface OperationsRetentionResponse {
  summary: OperationsRetentionSummary
  meta: OperationsDataMeta
}

export interface DailyCheckinAnalyticsPoint {
  date: string
  qualified_users: number
  checkin_users: number
  checkin_rate: number
  reward_usd: number
  avg_reward_usd: number
  fallback_count: number
  crit_count: number
  streak_user_count: number
}

export interface DailyCheckinRewardDistributionItem {
  label: string
  count: number
  reward_usd: number
}

export interface DailyCheckinAnalyticsSummary {
  qualified_users: number
  checkin_users: number
  streak_users: number
  checkin_rate: number
  reward_usd: number
  avg_reward_usd: number
  fallback_rate: number
  crit_rate: number
  streak_user_rate: number
  qualified_user_days: number
  checkin_user_days: number
  checkin_opportunity_rate: number
  daily_remaining_usd: number
  daily_remaining_rate?: number | null
  estimated_remaining_checkins?: number | null
  monthly_remaining_usd: number
  projected_budget_days?: number | null
}

export interface DailyCheckinAnalyticsResponse {
  summary: DailyCheckinAnalyticsSummary
  points: DailyCheckinAnalyticsPoint[]
  reward_distribution: DailyCheckinRewardDistributionItem[]
  meta: OperationsDataMeta
}

export type OperationsExportDataset = 'overview_daily' | 'daily_checkin_summary' | 'daily_checkin_records'

export type OperationsExportQuery = OperationsDateRangeQuery & DailyCheckinRecordQuery & {
  dataset: OperationsExportDataset
}

export async function getOperationsOverview(query: OperationsDateRangeQuery = {}): Promise<OperationsOverviewResponse> {
  const { data } = await apiClient.get<OperationsOverviewResponse>('/admin/operations/overview', { params: cleanParams(query) })
  return data
}

export async function getOperationsRetention(query: OperationsDateRangeQuery = {}): Promise<OperationsRetentionResponse> {
  const { data } = await apiClient.get<OperationsRetentionResponse>('/admin/operations/retention', { params: cleanParams(query) })
  return data
}

export async function getDailyCheckinAnalytics(query: OperationsDateRangeQuery = {}): Promise<DailyCheckinAnalyticsResponse> {
  const { data } = await apiClient.get<DailyCheckinAnalyticsResponse>('/admin/operations/daily-checkin/analytics', { params: cleanParams(query) })
  return data
}

export async function getDailyCheckinStats(query: Pick<OperationsDateRangeQuery, 'timezone'> = {}): Promise<OperationsAdminStats> {
	const { data } = await apiClient.get<OperationsAdminStats>('/admin/operations/daily-checkin/stats', { params: cleanParams(query) })
	return data
}

export async function updateDailyCheckinSettings(settings: DailyCheckinSettingsUpdateRequest): Promise<SystemSettings> {
  const { data } = await apiClient.put<SystemSettings>('/admin/operations/daily-checkin/settings', settings)
  return data
}

export async function listDailyCheckinRecords(query: DailyCheckinRecordQuery = {}): Promise<DailyCheckinRecordListResponse> {
  const { data } = await apiClient.get<DailyCheckinRecordListResponse>('/admin/operations/daily-checkin/records', { params: cleanParams(query) })
  return data
}

export async function exportOperationsData(query: OperationsExportQuery): Promise<Blob> {
  const { data } = await apiClient.get<Blob>('/admin/operations/export', {
    params: cleanParams(query),
    responseType: 'blob',
  })
  return data
}

function cleanParams(query: object): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries(query).filter(([, value]) => value !== undefined && value !== null && value !== '')
  )
}

export default {
  getOperationsOverview,
  getOperationsRetention,
  getDailyCheckinAnalytics,
  getDailyCheckinStats,
  updateDailyCheckinSettings,
  listDailyCheckinRecords,
  exportOperationsData,
}
