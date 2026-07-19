import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import Select from '@/components/common/Select.vue'
import AuditLogView from '../AuditLogView.vue'

const { listAuditLogs, showError } = vi.hoisted(() => ({
  listAuditLogs: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    audit: {
      list: listAuditLogs,
      get: vi.fn(),
      clear: vi.fn()
    }
  }
}))

vi.mock('@/api', () => ({
  totpAPI: { getStatus: vi.fn() }
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError, showSuccess: vi.fn() })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

describe('admin AuditLogView auth method filter', () => {
  beforeEach(() => {
    listAuditLogs.mockReset()
    showError.mockReset()
    listAuditLogs.mockResolvedValue({ items: [], total: 0 })
  })

  it('offers every auth method emitted by the backend', async () => {
    const wrapper = mount(AuditLogView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: {
            template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
          },
          DataTable: true,
          Pagination: true,
          BaseDialog: true,
          ConfirmDialog: true,
          Icon: true
        }
      }
    })
    await flushPromises()

    const authMethodSelect = wrapper.findAllComponents(Select).find((select) => {
      const options = select.props('options') as Array<{ value: string }>
      return options.some((option) => option.value === 'admin_api_key')
    })
    expect(authMethodSelect).toBeDefined()

    const options = authMethodSelect!.props('options') as Array<{ value: string }>
    expect(options.map((option) => option.value)).toEqual([
      '',
      'jwt',
      'admin_api_key',
      'password',
      'password_totp',
      'oauth',
      'oauth_totp'
    ])
    wrapper.unmount()
  })
})
