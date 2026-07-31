import { apiClient } from '../client'

export interface AsyncImageObject {
  key: string
  size: number
  etag?: string
  last_modified: string
  url: string
}

export interface AsyncImageObjectPage {
  items: AsyncImageObject[]
  next_cursor?: string
  has_more: boolean
  prefix: string
  bucket: string
}

export type AsyncImageTaskStatus = 'processing' | 'completed' | 'failed'

export interface AsyncImageTaskAdmin {
  id: string
  task_id: string
  user_id: number
  api_key_id: number
  platform?: string
  operation?: string
  model?: string
  image_count?: number
  result_count: number
  status: AsyncImageTaskStatus
  http_status?: number
  created_at: number
  completed_at?: number
  expires_at: number
  duration_ms: number
  error_type?: string
  stop_reason?: string
  result_urls?: string[]
}

export interface AsyncImageTaskStats {
  processing: number
  completed: number
  failed: number
}

export interface AsyncImageTaskPage {
  items: AsyncImageTaskAdmin[]
  next_cursor?: string
  has_more: boolean
  stats: AsyncImageTaskStats
  server_time: number
}

export async function list(params: { prefix?: string; cursor?: string; limit?: number } = {}) {
  const { data } = await apiClient.get<AsyncImageObjectPage>('/admin/image-generations', { params })
  return data
}

export async function listTasks(params: { status?: string; cursor?: string; limit?: number } = {}) {
  const { data } = await apiClient.get<AsyncImageTaskPage>('/admin/image-generations/tasks', { params })
  return data
}

export const imageGenerationsAPI = { list, listTasks }

export default imageGenerationsAPI
