import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick, reactive } from 'vue'

import AnnouncementPopup from '../AnnouncementPopup.vue'

const announcementState = reactive({
  currentPopup: null as null | {
    id: number
    title: string
    content: string
    created_at: string
  },
})

vi.mock('@/stores/announcements', () => ({
  useAnnouncementStore: () => ({
    get currentPopup() {
      return announcementState.currentPopup
    },
    dismissPopup: vi.fn(() => {
      announcementState.currentPopup = null
    }),
  }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('@/utils/format', () => ({
  formatRelativeWithDateTime: (value: string) => value,
}))

describe('AnnouncementPopup', () => {
  afterEach(() => {
    announcementState.currentPopup = null
    document.body.style.overflow = ''
  })

  it('restores the previous body overflow when popup closes', async () => {
    document.body.style.overflow = 'auto'
    const wrapper = mount(AnnouncementPopup, {
      global: {
        stubs: {
          Teleport: true,
          Transition: false,
        },
      },
    })

    announcementState.currentPopup = {
      id: 1,
      title: '公告',
      content: '内容',
      created_at: '2026-06-04T12:00:00Z',
    }
    await nextTick()
    expect(document.body.style.overflow).toBe('hidden')

    announcementState.currentPopup = null
    await nextTick()
    expect(document.body.style.overflow).toBe('auto')

    wrapper.unmount()
  })
})
