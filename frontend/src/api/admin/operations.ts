import { apiClient } from '../client'
import type { DailyCheckinAdminStats } from './settings'

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

export async function getDailyCheckinStats(): Promise<DailyCheckinAdminStats> {
  const { data } = await apiClient.get<DailyCheckinAdminStats>('/admin/operations/daily-checkin/stats')
  return data
}

export async function listDailyCheckinRecords(query: DailyCheckinRecordQuery = {}): Promise<DailyCheckinRecordListResponse> {
  const params = Object.fromEntries(
    Object.entries(query).filter(([, value]) => value !== undefined && value !== null && value !== '')
  )
  const { data } = await apiClient.get<DailyCheckinRecordListResponse>('/admin/operations/daily-checkin/records', { params })
  return data
}

export default {
  getDailyCheckinStats,
  listDailyCheckinRecords,
}
