import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, shallowMount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const {
  listAccounts,
  listWithEtag,
  getBatchTodayStats,
  getAllProxies,
  getAllGroups,
  getUpstreamBillingProbeSettings,
  updateUpstreamBillingProbeSettings,
  setArchived,
  bulkUpdate
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  getUpstreamBillingProbeSettings: vi.fn(),
  updateUpstreamBillingProbeSettings: vi.fn(),
  setArchived: vi.fn(),
  bulkUpdate: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag,
      getBatchTodayStats,
      getUpstreamBillingProbeSettings,
      updateUpstreamBillingProbeSettings,
      setArchived,
      bulkUpdate,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn()
    },
    proxies: {
      getAll: getAllProxies
    },
    groups: {
      getAll: getAllGroups
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    token: 'test-token'
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

const DataTableStub = {
  props: ['columns', 'data'],
  template: `
    <div data-test="data-table">
      <span v-for="column in columns" :key="column.key" data-test="column-key">{{ column.key }}</span>
      <div v-for="row in data" :key="row.id" data-test="account-row">
        <span data-test="account-name">{{ row.name }}</span>
        <slot name="cell-select" :row="row" />
        <slot name="cell-created_at" :value="row.created_at" :row="row" />
        <slot name="cell-actions" :row="row" />
      </div>
    </div>
  `
}

const AccountBulkActionsBarStub = {
  props: ['selectedIds'],
  emits: ['archive', 'edit-filtered'],
  template: `
    <div>
      <button data-test="bulk-archive" @click="$emit('archive')">archive</button>
      <button data-test="edit-filtered" @click="$emit('edit-filtered')">edit filtered</button>
    </div>
  `
}

const AccountActionMenuStub = {
  props: ['show', 'account'],
  emits: ['archive', 'unarchive', 'close'],
  template: `
    <div v-if="show" data-test="account-action-menu">
      <button v-if="account?.archived_at" data-test="unarchive-account" @click="$emit('unarchive', account)">unarchive</button>
      <button v-else data-test="archive-account" @click="$emit('archive', account)">archive</button>
    </div>
  `
}

const BulkEditAccountModalStub = {
  props: ['show', 'target'],
  template: '<div data-test="bulk-edit-modal" :data-show="String(show)" :data-target-mode="target?.mode ?? \'\'"></div>'
}

describe('admin AccountsView bulk edit scope', () => {
  beforeEach(() => {
    localStorage.clear()

    listAccounts.mockReset()
    listWithEtag.mockReset()
    getBatchTodayStats.mockReset()
    getAllProxies.mockReset()
    getAllGroups.mockReset()
    getUpstreamBillingProbeSettings.mockReset()
    updateUpstreamBillingProbeSettings.mockReset()
    setArchived.mockReset()
    bulkUpdate.mockReset()

    listAccounts.mockResolvedValue({
      items: [],
      total: 0,
      page: 1,
      page_size: 20,
      pages: 0
    })
    listWithEtag.mockResolvedValue({
      notModified: true,
      etag: null,
      data: null
    })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
    getUpstreamBillingProbeSettings.mockResolvedValue({ enabled: false, interval_minutes: 60 })
    updateUpstreamBillingProbeSettings.mockResolvedValue({ enabled: false, interval_minutes: 60 })
    setArchived.mockImplementation(async (_id: number, archived: boolean) => ({
      id: _id,
      name: `account-${_id}`,
      platform: 'anthropic',
      type: 'oauth',
      status: 'active',
      schedulable: true,
      archived_at: archived ? '2026-06-01T00:00:00Z' : null,
      created_at: '2026-03-07T10:00:00Z',
      updated_at: '2026-03-07T10:00:00Z'
    }))
    bulkUpdate.mockResolvedValue({
      success: 0,
      failed: 0,
      success_ids: [],
      failed_ids: [],
      results: []
    })
    vi.spyOn(window, 'confirm').mockReturnValue(true)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('keeps upstream billing probe controls inside the tools menu', async () => {
    const wrapper = shallowMount(AccountsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: {
            template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
          },
          AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
          Toggle: { template: '<button v-bind="$attrs" />' }
        }
      }
    })

    await flushPromises()
    await wrapper.get('button[title="admin.accounts.moreActions"]').trigger('click')

    const title = wrapper.get('[aria-label="admin.accounts.upstreamBilling.autoProbeSettings"]')
    const titleLabel = title.element.previousElementSibling as HTMLElement
    const intervalLabel = wrapper.get('label[for="upstream-billing-probe-interval"]')
    const intervalInput = wrapper.get('#upstream-billing-probe-interval')
    const saveButton = wrapper.get('button[title="common.save"]')

    expect(titleLabel.classList).toContain('min-w-0')
    expect(titleLabel.classList).toContain('break-words')
    expect(title.classes()).toContain('shrink-0')
    expect(intervalLabel.classes()).toContain('min-w-0')
    expect(intervalLabel.classes()).toContain('break-words')
    expect(intervalInput.classes()).toContain('shrink-0')
    expect(saveButton.classes()).toContain('shrink-0')
  })

  it('opens bulk edit in filtered-results mode from the bulk actions dropdown', async () => {
    const wrapper = mount(AccountsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: {
            template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
          },
          DataTable: DataTableStub,
          Pagination: true,
          ConfirmDialog: true,
          AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
          AccountTableFilters: { template: '<div></div>' },
          AccountBulkActionsBar: AccountBulkActionsBarStub,
          AccountActionMenu: AccountActionMenuStub,
          ImportDataModal: true,
          ReAuthAccountModal: true,
          AccountTestModal: true,
          AccountStatsModal: true,
          ScheduledTestsPanel: true,
          SyncFromCrsModal: true,
          TempUnschedStatusModal: true,
          ErrorPassthroughRulesModal: true,
          TLSFingerprintProfilesModal: true,
          CreateAccountModal: true,
          EditAccountModal: true,
          BulkEditAccountModal: BulkEditAccountModalStub,
          PlatformTypeBadge: true,
          AccountCapacityCell: true,
          AccountStatusIndicator: true,
          AccountTodayStatsCell: true,
          AccountGroupsCell: true,
          AccountUsageCell: true,
          Icon: true
        }
      }
    })

    await flushPromises()
    await wrapper.get('[data-test="edit-filtered"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-test="bulk-edit-modal"]').attributes('data-show')).toBe('true')
    expect(wrapper.get('[data-test="bulk-edit-modal"]').attributes('data-target-mode')).toBe('filtered')
  })

  it('renders the created_at column by default', async () => {
    listAccounts.mockResolvedValue({
      items: [
        {
          id: 1,
          name: 'test-account',
          platform: 'anthropic',
          type: 'oauth',
          status: 'active',
          schedulable: true,
          created_at: '2026-03-07T10:00:00Z',
          updated_at: '2026-03-07T10:00:00Z'
        }
      ],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })

    const wrapper = mount(AccountsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: {
            template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
          },
          DataTable: DataTableStub,
          Pagination: true,
          ConfirmDialog: true,
          AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
          AccountTableFilters: { template: '<div></div>' },
          AccountBulkActionsBar: AccountBulkActionsBarStub,
          AccountActionMenu: AccountActionMenuStub,
          ImportDataModal: true,
          ReAuthAccountModal: true,
          AccountTestModal: true,
          AccountStatsModal: true,
          ScheduledTestsPanel: true,
          SyncFromCrsModal: true,
          TempUnschedStatusModal: true,
          ErrorPassthroughRulesModal: true,
          TLSFingerprintProfilesModal: true,
          CreateAccountModal: true,
          EditAccountModal: true,
          BulkEditAccountModal: BulkEditAccountModalStub,
          PlatformTypeBadge: true,
          AccountCapacityCell: true,
          AccountStatusIndicator: true,
          AccountTodayStatsCell: true,
          AccountGroupsCell: true,
          AccountUsageCell: true,
          Icon: true
        }
      }
    })

    await flushPromises()

    const columnKeys = wrapper.findAll('[data-test="column-key"]').map(node => node.text())
    expect(columnKeys).toContain('created_at')
    const columns = wrapper.getComponent(DataTableStub).props('columns') as Array<{ key: string; label: string; sortable: boolean }>
    expect(columns.find(column => column.key === 'created_at')).toMatchObject({
      label: 'admin.accounts.columns.createdAt',
      sortable: true
    })
  })

  it('removes a single archived account from the default list after archive succeeds', async () => {
    listAccounts.mockResolvedValue({
      items: [
        {
          id: 1,
          name: 'visible-account',
          platform: 'anthropic',
          type: 'oauth',
          status: 'active',
          schedulable: true,
          created_at: '2026-03-07T10:00:00Z',
          updated_at: '2026-03-07T10:00:00Z'
        }
      ],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })
    setArchived.mockResolvedValue({
      id: 1,
      name: 'visible-account',
      platform: 'anthropic',
      type: 'oauth',
      status: 'active',
      schedulable: true,
      archived_at: '2026-06-01T00:00:00Z',
      created_at: '2026-03-07T10:00:00Z',
      updated_at: '2026-03-07T10:00:00Z'
    })

    const wrapper = mountAccountsView()

    await flushPromises()
    expect(wrapper.findAll('[data-test="account-row"]')).toHaveLength(1)

    const moreButton = wrapper.findAll('button').find(button => button.text().includes('common.more'))
    expect(moreButton).toBeTruthy()
    await moreButton!.trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="archive-account"]').trigger('click')
    await flushPromises()

    expect(setArchived).toHaveBeenCalledWith(1, true)
    expect(wrapper.findAll('[data-test="account-row"]')).toHaveLength(0)
  })

  it('removes only successful accounts after bulk archive partial success', async () => {
    listAccounts.mockResolvedValue({
      items: [
        {
          id: 1,
          name: 'archive-success',
          platform: 'anthropic',
          type: 'oauth',
          status: 'active',
          schedulable: true,
          created_at: '2026-03-07T10:00:00Z',
          updated_at: '2026-03-07T10:00:00Z'
        },
        {
          id: 2,
          name: 'archive-failed',
          platform: 'anthropic',
          type: 'oauth',
          status: 'active',
          schedulable: true,
          created_at: '2026-03-07T10:00:00Z',
          updated_at: '2026-03-07T10:00:00Z'
        }
      ],
      total: 2,
      page: 1,
      page_size: 20,
      pages: 1
    })
    bulkUpdate.mockResolvedValue({
      success: 1,
      failed: 1,
      success_ids: [1],
      failed_ids: [2],
      results: [
        { account_id: 1, success: true },
        { account_id: 2, success: false, error: 'archived account cannot be bulk updated' }
      ]
    })

    const wrapper = mountAccountsView()

    await flushPromises()
    const checkboxes = wrapper.findAll('input[type="checkbox"]')
    await checkboxes[0].setValue(true)
    await checkboxes[1].setValue(true)
    await wrapper.get('[data-test="bulk-archive"]').trigger('click')
    await flushPromises()

    expect(bulkUpdate).toHaveBeenCalledWith([1, 2], { archived: true })
    expect(wrapper.findAll('[data-test="account-name"]').map(node => node.text())).toEqual(['archive-failed'])
  })

  it('keeps the selected 5s auto refresh cadence after a local account update', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-06-10T00:00:00Z'))
    localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))

    const account = {
      id: 1,
      name: 'auto-refresh-account',
      platform: 'anthropic',
      type: 'oauth',
      status: 'active',
      schedulable: true,
      created_at: '2026-03-07T10:00:00Z',
      updated_at: '2026-03-07T10:00:00Z'
    }
    listAccounts.mockResolvedValue({
      items: [account],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })
    listWithEtag.mockResolvedValue({
      notModified: true,
      etag: 'etag-1',
      data: null
    })

    const wrapper = mountAccountsView()
    await flushPromises()

    ;(wrapper.vm as any).handleAccountUpdated({
      ...account,
      updated_at: '2026-06-10T00:00:01Z'
    })

    await vi.advanceTimersByTimeAsync(4999)
    await flushPromises()
    expect(listWithEtag).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(1)
    await flushPromises()
    expect(listWithEtag).toHaveBeenCalledTimes(1)
  })

  it('reschedules auto refresh by the selected interval after manual refresh', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-06-10T00:00:00Z'))
    localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))

    const account = {
      id: 1,
      name: 'manual-refresh-account',
      platform: 'anthropic',
      type: 'oauth',
      status: 'active',
      schedulable: true,
      created_at: '2026-03-07T10:00:00Z',
      updated_at: '2026-03-07T10:00:00Z'
    }
    listAccounts.mockResolvedValue({
      items: [account],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })
    listWithEtag.mockResolvedValue({
      notModified: true,
      etag: 'etag-1',
      data: null
    })

    const wrapper = mountAccountsView()
    await flushPromises()

    await vi.advanceTimersByTimeAsync(1000)
    await (wrapper.vm as any).handleManualRefresh()
    await flushPromises()

    await vi.advanceTimersByTimeAsync(4999)
    await flushPromises()
    expect(listWithEtag).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(1)
    await flushPromises()
    expect(listWithEtag).toHaveBeenCalledTimes(1)
  })
})

function mountAccountsView() {
  return mount(AccountsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
        },
        DataTable: DataTableStub,
        Pagination: true,
        ConfirmDialog: true,
        AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
        AccountTableFilters: { template: '<div></div>' },
        AccountBulkActionsBar: AccountBulkActionsBarStub,
        AccountActionMenu: AccountActionMenuStub,
        ImportDataModal: true,
        ReAuthAccountModal: true,
        AccountTestModal: true,
        AccountStatsModal: true,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        TempUnschedStatusModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        CreateAccountModal: true,
        EditAccountModal: true,
        BulkEditAccountModal: BulkEditAccountModalStub,
        PlatformTypeBadge: true,
        AccountCapacityCell: true,
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountGroupsCell: true,
        AccountUsageCell: true,
        Icon: true
      }
    }
  })
}
