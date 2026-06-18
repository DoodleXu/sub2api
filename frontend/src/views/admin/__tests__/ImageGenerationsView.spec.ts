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

describe('ImageGenerationsView', () => {
  let originalCreateObjectURL: typeof URL.createObjectURL
  let originalRevokeObjectURL: typeof URL.revokeObjectURL
  let originalOpen: typeof window.open
  let originalHTMLAnchorElementClick: typeof HTMLAnchorElement.prototype.click
  let intersectionCallback: IntersectionObserverCallback | null
  let intersectionObserver: IntersectionObserver | null
  let observedElements: Element[]
  let downloadedHref = ''
  let downloadedFilename = ''
  let objectURLCounter = 0

  beforeEach(() => {
    vi.clearAllMocks()
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-06-17T12:00:00Z'))
    localStorage.clear()
    localStorage.setItem('auth_token', 'admin-token')
    vi.stubGlobal('fetch', vi.fn(async () => new Response(new Blob(['png'], { type: 'image/png' }), {
      status: 200,
      headers: { 'Content-Type': 'image/png' },
    })))
    originalCreateObjectURL = URL.createObjectURL
    originalRevokeObjectURL = URL.revokeObjectURL
    originalOpen = window.open
    originalHTMLAnchorElementClick = HTMLAnchorElement.prototype.click
    objectURLCounter = 0
    URL.createObjectURL = vi.fn(() => `blob:admin-image-asset-${++objectURLCounter}`)
    URL.revokeObjectURL = vi.fn()
    window.open = vi.fn() as unknown as typeof window.open
    intersectionCallback = null
    intersectionObserver = null
    observedElements = []
    class MockIntersectionObserver {
      readonly root = null
      readonly rootMargin = ''
      readonly thresholds = []
      constructor(callback: IntersectionObserverCallback) {
        intersectionCallback = callback
        intersectionObserver = this as unknown as IntersectionObserver
      }
      observe = vi.fn((element: Element) => {
        observedElements.push(element)
      })
      unobserve = vi.fn((element: Element) => {
        observedElements = observedElements.filter((item) => item !== element)
      })
      disconnect = vi.fn(() => {
        observedElements = []
      })
      takeRecords = vi.fn(() => [])
    }
    vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)
    downloadedHref = ''
    downloadedFilename = ''
    HTMLAnchorElement.prototype.click = vi.fn(function (this: HTMLAnchorElement) {
      downloadedHref = this.href
      downloadedFilename = this.download
    })
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
    window.open = originalOpen
    HTMLAnchorElement.prototype.click = originalHTMLAnchorElementClick
  })

  async function revealThumbnail(wrapper: ReturnType<typeof mount>, assetID = 7) {
    const element = wrapper.get(`[data-testid="image-thumbnail-${assetID}"]`).element
    expect(observedElements).toContain(element)
    intersectionCallback?.([
      { isIntersecting: true, target: element } as IntersectionObserverEntry,
    ], intersectionObserver as IntersectionObserver)
    await flushPromises()
    await flushPromises()
  }

  it('lazy-loads admin image archive thumbnails from authenticated admin blobs', async () => {
    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()

    expect(fetch).not.toHaveBeenCalled()
    expect(wrapper.find('article img').exists()).toBe(false)
    expect(wrapper.text()).toContain('加载中...')

    await revealThumbnail(wrapper)

    const image = wrapper.find('article img')
    expect(image.attributes('src')).toBe('blob:admin-image-asset-1')
    expect(fetch).toHaveBeenCalledWith(`${adminAssetURL}?v=hash`, {
      credentials: 'include',
      headers: { Authorization: 'Bearer admin-token' },
    })
    expect(list).toHaveBeenCalledWith(expect.objectContaining({ page: 1, page_size: 60 }))
    expect(wrapper.text()).toContain('归档2.00 GB')

    await wrapper.find('article button').trigger('click')
    await flushPromises()

    const previewButtons = wrapper.findAll('button')
    expect(previewButtons.some((button) => button.text() === '打开')).toBe(true)
    expect(previewButtons.some((button) => button.text() === '下载')).toBe(true)
  })

  it('uses the versioned admin URL when a signed asset URL is not available', async () => {
    list.mockResolvedValueOnce({
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
              url: '',
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
    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()

    await revealThumbnail(wrapper)

    expect(fetch).toHaveBeenCalledWith(`${adminAssetURL}?v=hash`, {
      credentials: 'include',
      headers: { Authorization: 'Bearer admin-token' },
    })
    expect(wrapper.find('article img').attributes('src')).toBe('blob:admin-image-asset-1')
  })

  it('opens and downloads preview assets through the authenticated admin asset URL after signed links age out', async () => {
    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()
    await revealThumbnail(wrapper)

    vi.setSystemTime(new Date('2026-06-17T13:00:00Z'))
    await wrapper.find('article button').trigger('click')
    await flushPromises()

    const previewButtons = wrapper.findAll('button')
    await previewButtons.find((button) => button.text() === '打开')!.trigger('click')
    await flushPromises()

    expect(fetch).toHaveBeenLastCalledWith(`${adminAssetURL}?v=hash`, {
      credentials: 'include',
      headers: { Authorization: 'Bearer admin-token' },
    })
    expect(window.open).toHaveBeenCalledWith('blob:admin-image-asset-3', '_blank', 'noopener')

    await previewButtons.find((button) => button.text() === '下载')!.trigger('click')
    await flushPromises()

    expect(fetch).toHaveBeenLastCalledWith(`${adminAssetURL}?v=hash`, {
      credentials: 'include',
      headers: { Authorization: 'Bearer admin-token' },
    })
    expect(downloadedHref).toBe('blob:admin-image-asset-4')
    expect(downloadedFilename).toBe('image-generation-7.png')
    expect(URL.revokeObjectURL).not.toHaveBeenCalledWith('blob:admin-image-asset-3')
    expect(URL.revokeObjectURL).not.toHaveBeenCalledWith('blob:admin-image-asset-4')
    vi.advanceTimersByTime(60_000)
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:admin-image-asset-3')
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:admin-image-asset-4')
  })

  it('shows a retry state when an authenticated asset blob fails to load', async () => {
    vi.mocked(fetch).mockRejectedValueOnce(new Error('network down')).mockResolvedValueOnce(new Response(new Blob(['png'], { type: 'image/png' }), {
      status: 200,
      headers: { 'Content-Type': 'image/png' },
    }))

    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()

    await revealThumbnail(wrapper)

    expect(wrapper.text()).toContain('加载失败，点击重试')
    expect(wrapper.find('article img').exists()).toBe(false)

    await wrapper.find('article button').trigger('click')
    await flushPromises()
    await flushPromises()

    expect(fetch).toHaveBeenCalledTimes(2)
    expect(wrapper.find('article img').attributes('src')).toBe('blob:admin-image-asset-1')
  })

  it('does not fetch image blobs when records have no assets', async () => {
    list.mockResolvedValueOnce({
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
            image_count: 0,
            status: 'completed',
            storage_type: 'local',
            error_message: '',
            created_at: '2026-06-17T00:00:00Z',
          },
          assets: [],
        },
      ],
      total: 1,
      page: 1,
      page_size: 60,
      pages: 1,
    })

    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()

    expect(wrapper.text()).toContain('暂无生图资产')
    expect(fetch).not.toHaveBeenCalled()
  })

  it('releases preview object URLs when repeatedly opening and closing previews', async () => {
    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()
    await revealThumbnail(wrapper)

    await wrapper.find('article button').trigger('click')
    await flushPromises()
    expect(wrapper.find('.fixed img').attributes('src')).toBe('blob:admin-image-asset-2')

    await wrapper.findAll('button').find((button) => button.text() === '关闭')!.trigger('click')
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:admin-image-asset-2')
    expect(wrapper.find('.fixed').exists()).toBe(false)

    await wrapper.find('article button').trigger('click')
    await flushPromises()
    expect(wrapper.find('.fixed img').attributes('src')).toBe('blob:admin-image-asset-3')

    await wrapper.findAll('button').find((button) => button.text() === '关闭')!.trigger('click')
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:admin-image-asset-3')
    expect(URL.revokeObjectURL).not.toHaveBeenCalledWith('blob:admin-image-asset-1')
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
