import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import ImageGenerationsView from '../ImageGenerationsView.vue'

const {
  list,
  dailyStats,
  storageStats,
  clearAllArchives,
  getStorageConfig,
  updateStorageConfig,
  showSuccess,
  showWarning,
  showError,
} = vi.hoisted(() => ({
  list: vi.fn(),
  dailyStats: vi.fn(),
  storageStats: vi.fn(),
  clearAllArchives: vi.fn(),
  getStorageConfig: vi.fn(),
  updateStorageConfig: vi.fn(),
  showSuccess: vi.fn(),
  showWarning: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/admin/imageGenerations', () => ({
  default: {
    list,
    dailyStats,
    storageStats,
    clearAllArchives,
    getStorageConfig,
    updateStorageConfig,
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess,
    showWarning,
    showError,
  }),
}))

vi.mock('@/components/layout/AppLayout.vue', () => ({
  default: { template: '<div><slot /></div>' },
}))

const adminAssetURL = '/api/v1/admin/image-generations/assets/7'

describe('ImageGenerationsView', () => {
  let originalCreateObjectURL: typeof URL.createObjectURL
  let originalRevokeObjectURL: typeof URL.revokeObjectURL
  let originalOpen: typeof window.open
  let originalHTMLAnchorElementClick: typeof HTMLAnchorElement.prototype.click
  let intersectionCallback: IntersectionObserverCallback | null
  let intersectionObserver: IntersectionObserver | null
  let observedElements: Element[]
  let cacheEntries: Map<string, Response>
  let cacheMatch: ReturnType<typeof vi.fn>
  let cachePut: ReturnType<typeof vi.fn>
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
    cacheEntries = new Map()
    cacheMatch = vi.fn(async (request: RequestInfo | URL) => cacheEntries.get(String(request))?.clone())
    cachePut = vi.fn(async (request: RequestInfo | URL, response: Response) => {
      cacheEntries.set(String(request), response.clone())
    })
    vi.stubGlobal('caches', {
      open: vi.fn(async () => ({
        match: cacheMatch,
        put: cachePut,
      })),
    })
    vi.stubGlobal('confirm', vi.fn(() => true))
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
              url: adminAssetURL,
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
    clearAllArchives.mockResolvedValue({
      records_deleted: 1,
      assets_deleted: 1,
      storage_delete_failures: 0,
    })
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

  function expectAuthenticatedAssetFetch(url: string) {
    expect(fetch).toHaveBeenCalledWith(url, expect.objectContaining({
      credentials: 'include',
      headers: { Authorization: 'Bearer admin-token' },
    }))
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
    expectAuthenticatedAssetFetch(`${adminAssetURL}?v=hash`)
    expect(cacheMatch).toHaveBeenCalledWith(`${adminAssetURL}?v=hash`)
    expect(cachePut).toHaveBeenCalledWith(`${adminAssetURL}?v=hash`, expect.any(Response))
    expect((vi.mocked(fetch).mock.calls[0][1] as RequestInit).signal).toBeInstanceOf(AbortSignal)
    expect(list).toHaveBeenCalledWith(expect.objectContaining({ page: 1, page_size: 60 }))
    expect(list).toHaveBeenCalledWith(expect.objectContaining({ status: undefined }))
    expect(wrapper.text()).toContain('全部状态')
    expect(wrapper.text()).toContain('归档2.00 GB')

    await wrapper.find('article button').trigger('click')
    await flushPromises()

    const previewButtons = wrapper.findAll('button')
    expect(previewButtons.some((button) => button.text() === '打开')).toBe(true)
    expect(previewButtons.some((button) => button.text() === '下载')).toBe(true)
  })

  it('reuses Cache Storage for versioned admin image blobs', async () => {
    cacheEntries.set(`${adminAssetURL}?v=hash`, new Response(new Blob(['cached-png'], { type: 'image/png' }), {
      status: 200,
      headers: { 'Content-Type': 'image/png' },
    }))

    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()

    await revealThumbnail(wrapper)

    expect(cacheMatch).toHaveBeenCalledWith(`${adminAssetURL}?v=hash`)
    expect(fetch).not.toHaveBeenCalled()
    expect(cachePut).not.toHaveBeenCalled()
    expect(wrapper.find('article img').attributes('src')).toBe('blob:admin-image-asset-1')
  })

  it('limits visible thumbnail downloads to four concurrent requests', async () => {
    const resolvers: Array<(response: Response) => void> = []
    vi.mocked(fetch).mockImplementation(() => new Promise<Response>((resolve) => {
      resolvers.push(resolve)
    }))
    list.mockResolvedValueOnce({
      items: Array.from({ length: 6 }, (_, index) => {
        const id = index + 1
        return {
          record: {
            id,
            user_id: 3,
            api_key_id: 9,
            request_id: `req_${id}`,
            source: 'gateway',
            endpoint: '/v1/responses',
            model: 'gpt-image-2',
            prompt_excerpt: `image ${id}`,
            image_count: 1,
            status: 'completed',
            storage_type: 'local',
            error_message: '',
            created_at: '2026-06-17T00:00:00Z',
          },
          assets: [{
            id,
            record_id: id,
            asset_index: 0,
            mime_type: 'image/png',
            extension: '.png',
            bytes: 123,
            sha256: `hash-${id}`,
            url: `/api/v1/admin/image-generations/assets/${id}`,
            admin_url: `/api/v1/admin/image-generations/assets/${id}`,
            created_at: '2026-06-17T00:00:00Z',
          }],
        }
      }),
      total: 6,
      page: 1,
      page_size: 60,
      pages: 1,
    })

    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()

    const entries = [1, 2, 3, 4, 5, 6].map((assetID) => ({
      isIntersecting: true,
      target: wrapper.get(`[data-testid="image-thumbnail-${assetID}"]`).element,
    } as IntersectionObserverEntry))
    intersectionCallback?.(entries, intersectionObserver as IntersectionObserver)
    await flushPromises()
    await flushPromises()

    expect(fetch).toHaveBeenCalledTimes(4)

    resolvers[0](new Response(new Blob(['png'], { type: 'image/png' }), { status: 200 }))
    await flushPromises()
    await flushPromises()

    expect(fetch).toHaveBeenCalledTimes(5)
  })

  it('aborts in-flight thumbnail requests when filters change', async () => {
    const abortSignals: AbortSignal[] = []
    vi.mocked(fetch).mockImplementation((_url, init) => new Promise<Response>((_resolve, reject) => {
      const signal = (init as RequestInit).signal as AbortSignal
      abortSignals.push(signal)
      signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
    }))

    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()
    await revealThumbnail(wrapper)

    expect(abortSignals[0].aborted).toBe(false)

    await wrapper.findAll('select')[0].setValue('failed')
    await flushPromises()

    expect(abortSignals[0].aborted).toBe(true)
  })

  it('aborts in-flight thumbnail requests when the view unmounts', async () => {
    const abortSignals: AbortSignal[] = []
    vi.mocked(fetch).mockImplementation((_url, init) => new Promise<Response>((_resolve, reject) => {
      const signal = (init as RequestInit).signal as AbortSignal
      abortSignals.push(signal)
      signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
    }))

    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()
    await revealThumbnail(wrapper)

    wrapper.unmount()

    expect(abortSignals[0].aborted).toBe(true)
  })

  it('aborts in-flight thumbnail requests when clearing archives', async () => {
    const abortSignals: AbortSignal[] = []
    vi.mocked(fetch).mockImplementation((_url, init) => new Promise<Response>((_resolve, reject) => {
      const signal = (init as RequestInit).signal as AbortSignal
      abortSignals.push(signal)
      signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
    }))

    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()
    await revealThumbnail(wrapper)

    await wrapper.findAll('button').find((button) => button.text() === '清空所有归档')!.trigger('click')
    await flushPromises()

    expect(abortSignals[0].aborted).toBe(true)
  })

  it('keeps status filtering available for archive troubleshooting', async () => {
    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()

    await wrapper.findAll('select')[0].setValue('failed')
    await flushPromises()

    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({
      page: 1,
      page_size: 60,
      status: 'failed',
    }))
  })

  it('clears all image archives and refreshes the dashboard data', async () => {
    list
      .mockResolvedValueOnce({
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
                url: adminAssetURL,
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
      .mockResolvedValueOnce({
        items: [],
        total: 0,
        page: 1,
        page_size: 60,
        pages: 0,
      })
    storageStats
      .mockResolvedValueOnce({ total_bytes: 123 })
      .mockResolvedValueOnce({ total_bytes: 0 })

    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()
    await revealThumbnail(wrapper)

    await wrapper.findAll('button').find((button) => button.text() === '清空所有归档')!.trigger('click')
    await flushPromises()
    await flushPromises()

    expect(window.confirm).toHaveBeenCalledWith('确定要清空所有生图归档吗？该操作不可撤销。')
    expect(clearAllArchives).toHaveBeenCalledTimes(1)
    expect(list).toHaveBeenCalledTimes(2)
    expect(dailyStats).toHaveBeenCalledTimes(2)
    expect(storageStats).toHaveBeenCalledTimes(2)
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:admin-image-asset-1')
    expect(wrapper.text()).toContain('暂无生图资产')
    expect(showSuccess).toHaveBeenCalledWith('已清空 1 条归档记录、1 个资产')
  })

  it('keeps archive records retryable when storage cleanup partially fails', async () => {
    clearAllArchives.mockResolvedValueOnce({
      records_deleted: 0,
      assets_deleted: 1,
      storage_delete_failures: 2,
    })

    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()

    await wrapper.findAll('button').find((button) => button.text() === '清空所有归档')!.trigger('click')
    await flushPromises()
    await flushPromises()

    expect(clearAllArchives).toHaveBeenCalledTimes(1)
    expect(showWarning).toHaveBeenCalledWith('有 2 个存储对象清理失败，归档记录已保留，可稍后重试；本次已清理 1 个存储对象')
    expect(showSuccess).not.toHaveBeenCalled()
  })

  it('shows repeated archive assets with the same image hash as separate audit entries', async () => {
    list.mockResolvedValueOnce({
      items: [
        {
          record: {
            id: 43,
            user_id: 3,
            api_key_id: 9,
            request_id: 'req_2',
            source: 'gateway',
            endpoint: '/v1/responses',
            model: 'gpt-image-2',
            prompt_excerpt: 'tiny robot',
            image_count: 1,
            status: 'completed',
            storage_type: 'local',
            error_message: '',
            created_at: '2026-06-17T00:01:00Z',
          },
          assets: [
            {
              id: 8,
              record_id: 43,
              asset_index: 0,
              mime_type: 'image/png',
              extension: '.png',
              bytes: 123,
              sha256: 'same-hash',
              url: '/api/v1/admin/image-generations/assets/8',
              admin_url: '/api/v1/admin/image-generations/assets/8',
              created_at: '2026-06-17T00:01:00Z',
            },
          ],
        },
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
              sha256: 'same-hash',
              url: adminAssetURL,
              admin_url: adminAssetURL,
              created_at: '2026-06-17T00:00:00Z',
            },
          ],
        },
      ],
      total: 2,
      page: 1,
      page_size: 60,
      pages: 1,
    })

    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()

    expect(wrapper.findAll('article')).toHaveLength(2)
    expect(wrapper.find('[data-testid="image-thumbnail-8"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="image-thumbnail-7"]').exists()).toBe(true)

    await revealThumbnail(wrapper, 8)
    await revealThumbnail(wrapper, 7)

    expect(fetch).toHaveBeenCalledTimes(2)
    expectAuthenticatedAssetFetch('/api/v1/admin/image-generations/assets/8?v=same-hash')
    expectAuthenticatedAssetFetch(`${adminAssetURL}?v=same-hash`)
  })

  it('loads additional archive records instead of stopping at the first page', async () => {
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
            prompt_excerpt: 'first page',
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
              sha256: 'hash-1',
              url: adminAssetURL,
              admin_url: adminAssetURL,
              created_at: '2026-06-17T00:00:00Z',
            },
          ],
        },
      ],
      total: 2,
      page: 1,
      page_size: 60,
      pages: 2,
    })
    list.mockResolvedValueOnce({
      items: [
        {
          record: {
            id: 41,
            user_id: 4,
            api_key_id: 10,
            request_id: 'req_0',
            source: 'gateway',
            endpoint: '/v1/responses',
            model: 'gpt-image-1',
            prompt_excerpt: 'second page',
            image_count: 1,
            status: 'completed',
            storage_type: 'local',
            error_message: '',
            created_at: '2026-06-16T00:00:00Z',
          },
          assets: [
            {
              id: 6,
              record_id: 41,
              asset_index: 0,
              mime_type: 'image/png',
              extension: '.png',
              bytes: 456,
              sha256: 'hash-0',
              url: '/api/v1/admin/image-generations/assets/6',
              admin_url: '/api/v1/admin/image-generations/assets/6',
              created_at: '2026-06-16T00:00:00Z',
            },
          ],
        },
      ],
      total: 2,
      page: 2,
      page_size: 60,
      pages: 2,
    })

    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    await flushPromises()

    expect(wrapper.findAll('article')).toHaveLength(1)
    expect(wrapper.text()).toContain('已加载 1 / 2 条请求，显示 1 张归档资产')

    await wrapper.findAll('button').find((button) => button.text() === '加载更多')!.trigger('click')
    await flushPromises()
    await flushPromises()

    expect(list).toHaveBeenNthCalledWith(2, expect.objectContaining({ page: 2, page_size: 60 }))
    expect(wrapper.findAll('article')).toHaveLength(2)
    expect(wrapper.find('[data-testid="image-thumbnail-6"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('已加载 2 / 2 条请求，显示 2 张归档资产')
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

    expectAuthenticatedAssetFetch(`${adminAssetURL}?v=hash`)
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

    expect(fetch).toHaveBeenCalledTimes(1)
    expect(window.open).toHaveBeenCalledWith('blob:admin-image-asset-3', '_blank', 'noopener')

    await previewButtons.find((button) => button.text() === '下载')!.trigger('click')
    await flushPromises()

    expect(fetch).toHaveBeenCalledTimes(1)
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
    expect(wrapper.text()).toContain('今日请求')
    expect(wrapper.text()).toContain('今日请求0今日图片0今日失败0总归档12.0 MB')

    await wrapper.find('[data-testid="image-archive-enabled"]').trigger('click')
    await wrapper.find('button.btn-secondary').trigger('click')

    expect(updateStorageConfig).toHaveBeenCalledWith(expect.objectContaining({ enabled: true }))
  })
})
