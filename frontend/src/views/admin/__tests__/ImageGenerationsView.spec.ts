import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import ImageGenerationsView from '../ImageGenerationsView.vue'

const list = vi.hoisted(() => vi.fn())
vi.mock('@/api/admin/imageGenerations', () => ({ default: { list } }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<div><slot /></div>' } }))
vi.mock('@/components/icons/Icon.vue', () => ({ default: { template: '<span />' } }))

describe('ImageGenerationsView', () => {
  beforeEach(() => {
    list.mockReset()
    list.mockResolvedValue({
      bucket: 'async-images', prefix: 'images/', has_more: false, items: [
        { key: 'images/imgtask_1-0.png', size: 2048, etag: 'abc', last_modified: '2026-07-22T05:03:20Z', url: 'https://example.test/image.png' },
      ],
    })
  })

  it('shows objects from the upstream async image bucket without archive controls', async () => {
    const wrapper = mount(ImageGenerationsView)
    await flushPromises()
    expect(list).toHaveBeenCalledWith(expect.objectContaining({ limit: 60 }))
    expect(wrapper.text()).toContain('async-images')
    expect(wrapper.text()).toContain('imgtask_1-0.png')
    expect(wrapper.text()).toContain('2.0 KB')
    expect(wrapper.text()).not.toContain('启用归档')
    expect(wrapper.text()).not.toContain('清空终态归档')
  })
})
