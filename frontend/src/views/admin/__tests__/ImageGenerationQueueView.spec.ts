import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import ImageGenerationQueueView from '../ImageGenerationQueueView.vue'

const listTasks = vi.hoisted(() => vi.fn())

vi.mock('@/api/admin/imageGenerations', () => ({ default: { listTasks } }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<div><slot /></div>' } }))
vi.mock('@/components/icons/Icon.vue', () => ({ default: { template: '<span />' } }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, values?: Record<string, unknown>) => values ? `${key}:${JSON.stringify(values)}` : key,
  }),
}))

describe('ImageGenerationQueueView', () => {
  beforeEach(() => {
    vi.useRealTimers()
    listTasks.mockReset()
    listTasks.mockResolvedValue({
      items: [{
        id: 'imgtask_1', task_id: 'imgtask_1', user_id: 12, api_key_id: 34,
        platform: 'openai', operation: 'generation', model: 'gpt-image-1', image_count: 1,
        result_count: 0,
        status: 'failed', created_at: 1784092800, expires_at: 1784179200,
        duration_ms: 8200, http_status: 502, stop_reason: 'upstream timeout', result_urls: [],
      }],
      has_more: false,
      stats: { processing: 2, completed: 4, failed: 1 },
      server_time: 1784092900,
    })
  })

  it('loads and displays queue metadata and status counts', async () => {
    const wrapper = mount(ImageGenerationQueueView)
    await flushPromises()

    expect(listTasks).toHaveBeenCalledWith({ status: 'all', cursor: undefined, limit: 50 })
    expect(wrapper.text()).toContain('imgtask_1')
    expect(wrapper.text()).toContain('upstream timeout')
    expect(wrapper.text()).toContain('admin.imageGenerations.processing')
    expect(wrapper.text()).toContain('2')
    expect(wrapper.text()).not.toContain('prompt')
    expect(wrapper.findAll('h1')).toHaveLength(1)
  })

  it('keeps the current cursor when polling a later page', async () => {
    listTasks
      .mockResolvedValueOnce({ items: [], has_more: true, next_cursor: 'page-2', stats: { processing: 0, completed: 0, failed: 0 } })
      .mockResolvedValue({ items: [], has_more: false, stats: { processing: 0, completed: 0, failed: 0 } })
    const wrapper = mount(ImageGenerationQueueView)
    await flushPromises()
    await wrapper.get('[data-test="next-page"]').trigger('click')
    await flushPromises()

    expect(listTasks).toHaveBeenLastCalledWith({ status: 'all', cursor: 'page-2', limit: 50 })
  })

  it('refreshes in place without showing the table skeleton again', async () => {
    vi.useFakeTimers()
    const wrapper = mount(ImageGenerationQueueView)
    await flushPromises()

    expect(wrapper.find('[data-test="next-page"]').exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    expect(listTasks).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('imgtask_1')
    expect(wrapper.findAll('.animate-pulse')).toHaveLength(0)
  })

  it('stops polling when auto refresh is disabled', async () => {
    vi.useFakeTimers()
    const wrapper = mount(ImageGenerationQueueView)
    await flushPromises()
    const initialCalls = listTasks.mock.calls.length

    await wrapper.get('[role="switch"]').trigger('click')
    await vi.advanceTimersByTimeAsync(4000)

    expect(listTasks.mock.calls.length).toBe(initialCalls)
  })
})
