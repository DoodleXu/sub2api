import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import ImageGenerationsView from '../ImageGenerationsView.vue'

const {
  list,
  dailyStats,
  storageStats,
  getStorageConfig,
  updateStorageConfig,
} = vi.hoisted(() => ({
  list: vi.fn(),
  dailyStats: vi.fn(),
  storageStats: vi.fn(),
  getStorageConfig: vi.fn(),
  updateStorageConfig: vi.fn(),
}))

vi.mock('@/api/admin/imageGenerations', () => ({
  default: {
    list,
    dailyStats,
    storageStats,
    getStorageConfig,
    updateStorageConfig,
  },
}))

vi.mock('@/components/layout/AppLayout.vue', () => ({
  default: { template: '<div><slot /></div>' },
}))

const signedAssetURL = '/api/v1/image-assets/7?expires=1800000000&scope=admin-image-generation&sig=abc'
const adminAssetURL = '/api/v1/admin/image-generations/assets/7'
const cachedBlobURL = 'blob:image-asset-7'

function createImageResponse(body = 'png') {
  return new Response(new Blob([body], { type: 'image/png' }), {
    status: 200,
    headers: { 'Content-Type': 'image/png' },
  })
}

describe('ImageGenerationsView', () => {
  let cacheStore: Map<string, Response>
  let originalCreateObjectURL: typeof URL.createObjectURL
  let originalRevokeObjectURL: typeof URL.revokeObjectURL

  beforeEach(() => {
    vi.clearAllMocks()
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-06-17T12:00:00Z'))
    localStorage.clear()
    localStorage.setItem('auth_token', 'admin-token')
    cacheStore = new Map()
    vi.stubGlobal('fetch', vi.fn(async () => createImageResponse('from-server')))
    Object.defineProperty(window, 'caches', {
      configurable: true,
      value: {
        open: vi.fn(async () => ({
          match: vi.fn(async (url: string) => cacheStore.get(url)),
          put: vi.fn(async (url: string, response: Response) => {
            cacheStore.set(url, response)
          }),
        })),
      },
    })
    originalCreateObjectURL = URL.createObjectURL
    originalRevokeObjectURL = URL.revokeObjectURL
    URL.createObjectURL = vi.fn(() => cachedBlobURL)
    URL.revokeObjectURL = vi.fn()
    list.mockResolvedValue({
      items: [
        {
          record: {
            id: 42,
            user_id: 3,
            api_key_id: 9,
            request_id: 'req_1',
            source: 'gateway',
            endpoint: '/v1/responses',
            model: 'gpt-image-2',
            prompt_excerpt: 'tiny robot',
            image_count: 1,
            status: 'completed',
            storage_type: 'local',
            error_message: '',
            created_at: '2026-06-17T00:00:00Z',
          },
          assets: [
            {
              id: 7,
              record_id: 42,
              asset_index: 0,
              mime_type: 'image/png',
              extension: '.png',
              bytes: 123,
              sha256: 'hash',
              url: signedAssetURL,
              admin_url: adminAssetURL,
              created_at: '2026-06-17T00:00:00Z',
            },
          ],
        },
      ],
      total: 1,
      page: 1,
      page_size: 60,
      pages: 1,
    })
    dailyStats.mockResolvedValue([
      { date: '2026-06-17', request_count: 1, image_count: 1, failed_count: 0 },
    ])
    storageStats.mockResolvedValue({ total_bytes: 2147483648 })
    getStorageConfig.mockResolvedValue({
      enabled: true,
      type: 'local',
      local_dir: './data/image-archive',
    })
    updateStorageConfig.mockResolvedValue({
      enabled: true,
      type: 'local',
      local_dir: './data/image-archive',
    })
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
    URL.createObjectURL = originalCreateObjectURL
    URL.revokeObjectURL = originalRevokeObjectURL
  })

  it('renders admin image archive assets from locally cached blobs', async () => {
    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()

    const image = wrapper.find('article img')
    expect(image.attributes('src')).toBe(cachedBlobURL)
    expect(fetch).toHaveBeenCalledWith(`${adminAssetURL}?v=hash`, {
      credentials: 'include',
      headers: { Authorization: 'Bearer admin-token' },
    })
    expect(cacheStore.has(`${adminAssetURL}?v=hash`)).toBe(true)
    expect(list).toHaveBeenCalledWith(expect.objectContaining({ page: 1, page_size: 60 }))
    expect(wrapper.text()).toContain('归档2.00 GB')

    await wrapper.find('article button').trigger('click')

    const previewLinks = wrapper.findAll('a')
    expect(previewLinks.map((link) => link.attributes('href'))).toEqual([signedAssetURL, signedAssetURL])
  })

  it('reuses Cache Storage when an image asset was already cached', async () => {
    cacheStore.set(`${adminAssetURL}?v=hash`, createImageResponse('from-cache'))

    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()

    expect(fetch).not.toHaveBeenCalled()
    expect(wrapper.find('article img').attributes('src')).toBe(cachedBlobURL)
  })

  it('saves archive enabled state and only shows stats for today', async () => {
    dailyStats.mockResolvedValue([
      { date: '2026-06-16', request_count: 9, image_count: 9, failed_count: 1 },
    ])
    storageStats.mockResolvedValue({ total_bytes: 12582912 })
    getStorageConfig.mockResolvedValue({
      enabled: false,
      type: 'local',
      local_dir: './data/image-archive',
    })
    updateStorageConfig.mockResolvedValue({
      enabled: false,
      type: 'local',
      local_dir: './data/image-archive',
    })

    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()

    expect(wrapper.text()).toContain('启用归档')
    expect(wrapper.text()).toContain('请求')
    expect(wrapper.text()).toContain('请求0图片0失败0归档12.0 MB')

    await wrapper.find('[data-testid="image-archive-enabled"]').trigger('click')
    await wrapper.find('button.btn-secondary').trigger('click')

    expect(updateStorageConfig).toHaveBeenCalledWith(expect.objectContaining({ enabled: true }))
  })
})
