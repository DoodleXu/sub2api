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

export async function list(params: { prefix?: string; cursor?: string; limit?: number } = {}) {
  const { data } = await apiClient.get<AsyncImageObjectPage>('/admin/image-generations', { params })
  return data
}

export const imageGenerationsAPI = { list }

export default imageGenerationsAPI
