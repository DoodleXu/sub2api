import { apiClient } from '../client'

export interface ImageGenerationAsset {
  id: number
  record_id: number
  asset_index: number
  mime_type: string
  extension: string
  width?: number | null
  height?: number | null
  bytes: number
  sha256: string
  url: string
  admin_url: string
  thumbnail_url?: string
  thumbnail_admin_url?: string
  created_at: string
}

export interface ImageGenerationRecord {
  id: number
  user_id?: number | null
  api_key_id?: number | null
  group_id?: number | null
  account_id?: number | null
  request_id: string
  source: string
  endpoint: string
  model: string
  prompt_excerpt: string
  image_count: number
  status: string
  storage_type: string
  error_message: string
  created_at: string
  completed_at?: string | null
}

export interface ImageGenerationListItem {
  record: ImageGenerationRecord
  assets: ImageGenerationAsset[]
}

export interface ImageGenerationDailyStat {
  date: string
  request_count: number
  image_count: number
  failed_count: number
}

export interface ImageGenerationStorageStats {
  total_bytes: number
}

export interface ImageGenerationArchiveClearResult {
  records_deleted: number
  assets_deleted: number
  storage_delete_failures: number
}

export interface ImageArchiveStorageConfig {
  enabled: boolean
  type: 'local' | 's3'
  local_dir: string
  s3_endpoint?: string
  s3_region?: string
  s3_bucket?: string
  s3_access_key?: string
  s3_secret_key?: string
  s3_prefix?: string
  public_base_url?: string
  path_style?: boolean
}

export async function list(params: Record<string, unknown>) {
  const { data } = await apiClient.get<{
    items: ImageGenerationListItem[]
    total: number
    page: number
    page_size: number
    pages: number
  }>('/admin/image-generations', { params })
  return data
}

export async function dailyStats(params: Record<string, unknown>) {
  const { data } = await apiClient.get<{ items: ImageGenerationDailyStat[] }>('/admin/image-generations/stats/daily', { params })
  return data.items
}

export async function storageStats() {
  const { data } = await apiClient.get<ImageGenerationStorageStats>('/admin/image-generations/stats/storage')
  return data
}

export async function clearAllArchives() {
  const { data } = await apiClient.delete<ImageGenerationArchiveClearResult>('/admin/image-generations')
  return data
}

export async function getStorageConfig() {
  const { data } = await apiClient.get<ImageArchiveStorageConfig>('/admin/settings/image-archive-storage')
  return data
}

export async function updateStorageConfig(payload: ImageArchiveStorageConfig) {
  const { data } = await apiClient.put<ImageArchiveStorageConfig>('/admin/settings/image-archive-storage', payload)
  return data
}

export const imageGenerationsAPI = {
  list,
  dailyStats,
  storageStats,
  clearAllArchives,
  getStorageConfig,
  updateStorageConfig,
}

export default imageGenerationsAPI
