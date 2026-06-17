import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import ImageGenerationsView from '../ImageGenerationsView.vue'

const {
  list,
  dailyStats,
  getStorageConfig,
  updateStorageConfig,
} = vi.hoisted(() => ({
  list: vi.fn(),
  dailyStats: vi.fn(),
  getStorageConfig: vi.fn(),
  updateStorageConfig: vi.fn(),
}))

vi.mock('@/api/admin/imageGenerations', () => ({
  default: {
    list,
    dailyStats,
    getStorageConfig,
    updateStorageConfig,
  },
}))

vi.mock('@/components/layout/AppLayout.vue', () => ({
  default: { template: '<div><slot /></div>' },
}))

const signedAssetURL = '/api/v1/image-assets/7?expires=1800000000&scope=admin-image-generation&sig=abc'

describe('ImageGenerationsView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-06-17T12:00:00Z'))
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
              admin_url: '/api/v1/admin/image-generations/assets/7',
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
  })

  it('renders admin image archive assets with signed image URLs', async () => {
    const wrapper = mount(ImageGenerationsView)
    await flushPromises()

    const image = wrapper.find('article img')
    expect(image.attributes('src')).toBe(signedAssetURL)
    expect(list).toHaveBeenCalledWith(expect.objectContaining({ page: 1, page_size: 60 }))

    await wrapper.find('article button').trigger('click')

    const previewLinks = wrapper.findAll('a')
    expect(previewLinks.map((link) => link.attributes('href'))).toEqual([signedAssetURL, signedAssetURL])
  })

  it('saves archive enabled state and only shows stats for today', async () => {
    dailyStats.mockResolvedValue([
      { date: '2026-06-16', request_count: 9, image_count: 9, failed_count: 1 },
    ])
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

    expect(wrapper.text()).toContain('启用归档')
    expect(wrapper.text()).toContain('请求')
    expect(wrapper.text()).toContain('请求0图片0失败0')

    await wrapper.find('[data-testid="image-archive-enabled"]').trigger('click')
    await wrapper.find('button.btn-secondary').trigger('click')

    expect(updateStorageConfig).toHaveBeenCalledWith(expect.objectContaining({ enabled: true }))
  })
})
