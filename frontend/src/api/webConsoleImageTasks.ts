import { apiClient } from './client'
import type { WebConsoleImageOptions } from '@/features/web-console/types'

export interface CreateWebConsoleImageTaskRequest {
  api_key_id: number
  endpoint: string
  model: string
  prompt: string
  options: WebConsoleImageOptions
  session_id?: string
  message_id?: string
}

export interface WebConsoleImageTaskAsset {
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
}

export interface WebConsoleImageTask {
  id: number
  status: 'pending' | 'running' | 'completed' | 'failed'
  record_id?: number | null
  error_message?: string
  assets: WebConsoleImageTaskAsset[]
}

export async function create(request: CreateWebConsoleImageTaskRequest): Promise<{ task: WebConsoleImageTask }> {
  const { data } = await apiClient.post<{ task: WebConsoleImageTask }>('/web-console/image-tasks', request)
  return data
}

export async function get(id: number): Promise<WebConsoleImageTask> {
  const { data } = await apiClient.get<WebConsoleImageTask>(`/web-console/image-tasks/${id}`)
  return data
}

export const webConsoleImageTasksAPI = { create, get }

export default webConsoleImageTasksAPI
