import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import EmailUnsubscribeView from '@/views/public/EmailUnsubscribeView.vue'

const { routeState, unsubscribeMock } = vi.hoisted(() => ({
  routeState: {
    query: {} as Record<string, unknown>,
  },
  unsubscribeMock: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => routeState,
}))

vi.mock('@/api/auth', () => ({
  unsubscribeNotificationEmail: (...args: any[]) => unsubscribeMock(...args),
}))

vi.mock('@/components/icons/Icon.vue', () => ({
  default: {
    props: ['name'],
    template: '<span data-test="icon">{{ name }}</span>',
  },
}))

function mountView() {
  return mount(EmailUnsubscribeView, {
    global: {
      stubs: {
        RouterLink: {
          template: '<a><slot /></a>',
        },
      },
    },
  })
}

describe('EmailUnsubscribeView', () => {
  beforeEach(() => {
    routeState.query = {}
    unsubscribeMock.mockReset()
  })

  it('renders success state after unsubscribing with token', async () => {
    routeState.query = { token: 'tok_123' }
    unsubscribeMock.mockResolvedValue({
      event: 'admin.broadcast_email',
      event_label: 'Admin email notification',
      email: 'user@example.com',
      done: true,
    })

    const wrapper = mountView()
    await flushPromises()

    expect(unsubscribeMock).toHaveBeenCalledWith('tok_123')
    expect(wrapper.text()).toContain('退订成功')
    expect(wrapper.text()).toContain('user@example.com')
    expect(wrapper.text()).toContain('Admin email notification')
  })

  it('renders invalid-link state when token is missing', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(unsubscribeMock).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('退订失败')
    expect(wrapper.text()).toContain('缺少 token')
  })

  it('renders API errors', async () => {
    routeState.query = { token: 'expired' }
    unsubscribeMock.mockRejectedValue({ message: 'unsubscribe token expired' })

    const wrapper = mountView()
    await flushPromises()

    expect(unsubscribeMock).toHaveBeenCalledWith('expired')
    expect(wrapper.text()).toContain('退订失败')
    expect(wrapper.text()).toContain('unsubscribe token expired')
  })
})
