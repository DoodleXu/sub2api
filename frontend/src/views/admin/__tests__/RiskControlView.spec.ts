import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import type { DOMWrapper, VueWrapper } from '@vue/test-utils'

import RiskControlView from '../RiskControlView.vue'
import type { ContentModerationConfig, ContentModerationLog, UpdateContentModerationConfig } from '@/api/admin/riskControl'

const {
  getConfig,
  updateConfig,
  getStatus,
  listLogs,
  listAllowedHashes,
  listUserPolicies,
  getGroups,
  getUserById,
  listUsers,
  allowHash,
  deleteAllowedHash,
  deleteFlaggedHash,
  clearAllowedHashes,
  showError,
  showSuccess,
} = vi.hoisted(() => ({
  getConfig: vi.fn(),
  updateConfig: vi.fn(),
  getStatus: vi.fn(),
  listLogs: vi.fn(),
  listAllowedHashes: vi.fn(),
  listUserPolicies: vi.fn(),
  getGroups: vi.fn(),
  getUserById: vi.fn(),
  listUsers: vi.fn(),
  allowHash: vi.fn(),
  deleteAllowedHash: vi.fn(),
  deleteFlaggedHash: vi.fn(),
  clearAllowedHashes: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    riskControl: {
      getConfig,
      updateConfig,
      getStatus,
      listLogs,
      listAllowedHashes,
      listUserPolicies,
      testAPIKeys: vi.fn(),
      allowHash,
      deleteAllowedHash,
      deleteFlaggedHash,
      clearFlaggedHashes: vi.fn(),
      clearAllowedHashes,
      unbanUser: vi.fn(),
    },
    groups: {
      getAll: getGroups,
    },
    users: {
      getById: getUserById,
      list: listUsers,
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
  }),
}))

vi.mock('@/utils/apiError', () => ({
  extractApiErrorMessage: (_err: unknown, fallback: string) => fallback,
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard: vi.fn().mockResolvedValue(true),
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string | number>) => {
        if (key === 'admin.riskControl.preBlockAPIKeyLoadSummary') {
          return `同步并发 ${params?.active} / 可用 Key ${params?.available}，累计 ${params?.total} 次，worker：${params?.workerActive} / ${params?.workerTotal}`
        }
        return key.replace(/\{(\w+)\}/g, (_, token) => String(params?.[token] ?? `{${token}}`))
      },
    }),
  }
})

const baseConfig = (): ContentModerationConfig => ({
  enabled: true,
  mode: 'pre_block',
  base_url: 'https://api.openai.com',
  model: 'omni-moderation-latest',
  api_key_configured: false,
  api_key_masked: '',
  api_key_count: 0,
  api_key_masks: [],
  api_key_statuses: [],
  timeout_ms: 3000,
  sample_rate: 100,
  all_groups: true,
  group_ids: [],
  whitelist_user_ids: [7],
  forced_whitelist_user_ids: [7],
  record_non_hits: false,
  worker_count: 4,
  queue_size: 32768,
  block_status: 403,
  block_message: '内容审计命中风险规则，请调整输入后重试',
  email_on_hit: true,
  auto_ban_enabled: true,
  ban_threshold: 10,
  violation_window_hours: 720,
  retry_count: 2,
  hit_retention_days: 180,
  non_hit_retention_days: 3,
  pre_hash_check_enabled: false,
  blocked_keywords: [],
  keyword_blocking_mode: 'keyword_and_api',
  thresholds: {
    harassment: 0.98,
    sexual: 0.65,
  },
  model_filter: {
    type: 'all',
    models: [],
  },
})

const runtimeStatus = () => ({
  enabled: true,
  risk_control_enabled: true,
  mode: 'pre_block',
  worker_count: 4,
  max_workers: 32,
  active_workers: 0,
  idle_workers: 4,
  queue_size: 32768,
  queue_length: 0,
  queue_usage_percent: 0,
  enqueued: 0,
  dropped: 0,
  processed: 0,
  errors: 0,
  pre_block_active: 0,
  pre_block_checked: 0,
  pre_block_allowed: 0,
  pre_block_blocked: 0,
  pre_block_errors: 0,
  pre_block_avg_latency_ms: 0,
  pre_block_api_key_active: 0,
  pre_block_api_key_available_count: 0,
  pre_block_api_key_total_calls: 0,
  pre_block_api_key_loads: [],
  api_key_statuses: [],
  flagged_hash_count: 0,
  allowed_hash_count: 0,
  last_cleanup_deleted_hit: 0,
  last_cleanup_deleted_non_hit: 0,
})

const AppLayoutStub = { template: '<div><slot /></div>' }
const BaseDialogStub = defineComponent({
  props: {
    show: {
      type: Boolean,
      default: false,
    },
    title: {
      type: String,
      default: '',
    },
  },
  template: '<div v-if="show"><h2>{{ title }}</h2><slot /><slot name="footer" /></div>',
})
const ModelWhitelistSelectorStub = defineComponent({
  props: {
    modelValue: {
      type: Array,
      default: () => [],
    },
  },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    const onInput = (event: Event) => {
      const value = (event.target as HTMLInputElement).value
      emit(
        'update:modelValue',
        value
          .split(/[,\n]/)
          .map((item) => item.trim())
          .filter(Boolean)
      )
    }
    return () =>
      h('input', {
        'data-test': 'model-filter-input',
        value: (props.modelValue as string[]).join('\n'),
        onInput,
      })
  },
})

const moderationLog = (overrides: Partial<ContentModerationLog> = {}): ContentModerationLog => ({
  id: 1,
  request_id: 'req-1',
  user_id: 1001,
  user_email: 'user@example.com',
  api_key_id: 10,
  api_key_name: 'primary key',
  group_id: 2,
  group_name: 'default',
  endpoint: '/v1/responses',
  provider: 'openai',
  model: 'gpt-5.5',
  mode: 'pre_block',
  action: 'allow',
  flagged: false,
  highest_category: '',
  highest_score: 0,
  category_scores: {},
  threshold_snapshot: {},
  policy_id: null,
  policy_action: '',
  policy_snapshot: {},
  block_status: 0,
  error_code: '',
  input_excerpt: 'normal summary',
  input_hash: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  matched_keyword: '',
  upstream_latency_ms: null,
  error: '',
  violation_count: 0,
  auto_banned: false,
  email_sent: false,
  user_status: 'active',
  queue_delay_ms: null,
  created_at: '2026-06-05T03:43:09Z',
  ...overrides,
})

function findButtonByText(wrapper: VueWrapper, text: string): DOMWrapper<HTMLButtonElement> {
  const button = wrapper.findAll<HTMLButtonElement>('button').find((item) => item.text().includes(text))
  if (!button) {
    throw new Error(`button not found: ${text}`)
  }
  return button
}

function mountRiskControlView() {
  return mount(RiskControlView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        BaseDialog: BaseDialogStub,
        Icon: true,
        Select: true,
        Toggle: true,
        Pagination: true,
        ModelWhitelistSelector: ModelWhitelistSelectorStub,
      },
    },
  })
}

describe('admin RiskControlView', () => {
  beforeEach(() => {
    getConfig.mockReset()
    updateConfig.mockReset()
    getStatus.mockReset()
    listLogs.mockReset()
    listAllowedHashes.mockReset()
    listUserPolicies.mockReset()
    getGroups.mockReset()
    getUserById.mockReset()
    listUsers.mockReset()
    allowHash.mockReset()
    deleteAllowedHash.mockReset()
    deleteFlaggedHash.mockReset()
    clearAllowedHashes.mockReset()
    showError.mockReset()
    showSuccess.mockReset()

    getConfig.mockResolvedValue(baseConfig())
    getStatus.mockResolvedValue(runtimeStatus())
    listLogs.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 1 })
    listAllowedHashes.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 10, pages: 1 })
    listUserPolicies.mockResolvedValue([])
    getGroups.mockResolvedValue([])
    getUserById.mockImplementation(async (id: number) => ({
      id,
      email: id === 7 ? 'admin@example.com' : `user-${id}@example.com`,
      username: id === 7 ? 'admin' : `user-${id}`,
      role: id === 7 ? 'admin' : 'user',
      status: 'active',
    }))
    listUsers.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 10, pages: 1 })
    allowHash.mockResolvedValue({
      input_hash: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
      added: true,
    })
    updateConfig.mockImplementation(async (payload: UpdateContentModerationConfig) => ({
      ...baseConfig(),
      ...payload,
      model_filter: payload.model_filter ?? baseConfig().model_filter,
      api_key_configured: false,
      api_key_masked: '',
      api_key_count: 0,
      api_key_masks: [],
      api_key_statuses: [],
    }))
  })

  it('shows matched keyword and full input for keyword-blocked logs', async () => {
    const fullInput = 'pullPage 这个函数有问题，触发 secret-token 后需要展示完整输入内容'
    listLogs.mockResolvedValue({
      items: [
        moderationLog({
          action: 'keyword_block',
          flagged: true,
          highest_category: 'keyword',
          highest_score: 1,
          input_excerpt: fullInput,
          matched_keyword: 'secret-token',
        }),
      ],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    })

    const wrapper = mountRiskControlView()
    await flushPromises()

    expect(wrapper.text()).toContain('admin.riskControl.matchedKeyword')
    expect(wrapper.text()).toContain('secret-token')
    expect(wrapper.text()).toContain(fullInput)

    await findButtonByText(wrapper, fullInput).trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('admin.riskControl.inputDetailTitle')
    expect(wrapper.text()).toContain('admin.riskControl.inputDetailContent')
    expect(wrapper.text()).toContain(fullInput)
  })

  it('keeps normal allowed logs as full input content in the input dialog', async () => {
    listLogs.mockResolvedValue({
      items: [moderationLog({ input_excerpt: 'normal request summary' })],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    })

    const wrapper = mountRiskControlView()
    await flushPromises()

    await findButtonByText(wrapper, 'normal request summary').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('admin.riskControl.inputDetailTitle')
    expect(wrapper.text()).toContain('admin.riskControl.inputDetailContent')
  })

  it('allows a log hash directly from the audit table', async () => {
    const inputHash = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
    allowHash.mockResolvedValue({ input_hash: inputHash, added: true })
    listLogs.mockResolvedValue({
      items: [moderationLog({ input_hash: inputHash })],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    })

    const wrapper = mountRiskControlView()
    await flushPromises()

    await findButtonByText(wrapper, 'admin.riskControl.allowHashShort').trigger('click')
    await flushPromises()

    expect(allowHash).toHaveBeenCalledWith({
      input_hash: inputHash,
      source_log_id: 1,
      note: undefined,
    })
    expect(showSuccess).toHaveBeenCalledWith('admin.riskControl.allowedHashAdded')
  })

  it('loads allowlisted hashes and clears them after confirmation', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)
    listAllowedHashes.mockResolvedValue({
      items: [
        {
          input_hash: 'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
          source: 'manual',
          input_excerpt: 'reviewed prompt',
          created_by_email: 'admin@example.com',
          note: 'false positive',
          created_at: '2026-06-05T03:43:09Z',
        },
      ],
      total: 1,
      page: 1,
      page_size: 10,
      pages: 1,
    })
    clearAllowedHashes.mockResolvedValue({ deleted: 1 })
    getStatus
      .mockResolvedValueOnce({ ...runtimeStatus(), allowed_hash_count: 1 })
      .mockResolvedValue({ ...runtimeStatus(), allowed_hash_count: 0 })

    const wrapper = mountRiskControlView()
    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.runtime').trigger('click')
    await flushPromises()

    expect(listAllowedHashes).toHaveBeenCalled()
    expect(wrapper.text()).toContain('false positive')

    await findButtonByText(wrapper, 'admin.riskControl.clearAllowedHashes').trigger('click')
    await flushPromises()

    expect(confirmSpy).toHaveBeenCalledWith('admin.riskControl.clearAllowedHashesConfirm')
    expect(clearAllowedHashes).toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalledWith('admin.riskControl.allowedHashesCleared')
    confirmSpy.mockRestore()
  })

  it('saves the selected model filter mode and models', async () => {
    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
        },
      },
    })

    await flushPromises()

    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.scope').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.modelFilterInclude').trigger('click')
    await wrapper.get('[data-test="model-filter-input"]').setValue('gpt-5.5, gpt-5.4')
    await findButtonByText(wrapper, 'admin.riskControl.saveConfig').trigger('click')
    await flushPromises()

    expect(updateConfig).toHaveBeenCalledWith(expect.objectContaining({
      model_filter: {
        type: 'include',
        models: ['gpt-5.5', 'gpt-5.4'],
      },
    }))
    expect(showError).not.toHaveBeenCalled()
  })

  it('submits edited risk control thresholds when saving moderation config', async () => {
    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
        },
      },
    })

    await flushPromises()

    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.riskThresholds').trigger('click')
    await wrapper.get('[data-test="risk-threshold-sexual"]').setValue('72')
    await wrapper.get('[data-test="risk-threshold-harassment"]').setValue('99')
    await findButtonByText(wrapper, 'admin.riskControl.saveConfig').trigger('click')
    await flushPromises()

    expect(updateConfig).toHaveBeenCalledWith(expect.objectContaining({
      thresholds: expect.objectContaining({
        sexual: 0.72,
        harassment: 0.99,
      }),
    }))
    expect(showError).not.toHaveBeenCalled()
  })

  it('shows readable whitelist users, keeps forced admins, and saves selected ids', async () => {
    listUsers.mockResolvedValue({
      items: [
        {
          id: 42,
          email: 'alice@example.com',
          username: 'alice',
          role: 'user',
          status: 'active',
        },
      ],
      total: 1,
      page: 1,
      page_size: 10,
      pages: 1,
    })
    const wrapper = mountRiskControlView()

    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.scope').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('admin@example.com')
    expect(wrapper.text()).toContain('admin.riskControl.forcedWhitelist')
    expect(wrapper.findAll('[data-test="whitelist-user-chip"]')).toHaveLength(1)

    await wrapper.get('[data-test="whitelist-user-search"]').setValue('alice')
    await new Promise((resolve) => window.setTimeout(resolve, 350))
    await flushPromises()

    const result = wrapper.get('[data-test="whitelist-user-result"]')
    expect(result.text()).toContain('alice@example.com')
    expect(result.attributes('disabled')).toBeUndefined()
    await result.trigger('click')
    expect(wrapper.findAll('[data-test="whitelist-user-chip"]')).toHaveLength(2)
    await findButtonByText(wrapper, 'admin.riskControl.saveConfig').trigger('click')
    await flushPromises()

    expect(updateConfig).toHaveBeenCalledWith(expect.objectContaining({
      whitelist_user_ids: [7, 42],
    }))
    expect(showError).not.toHaveBeenCalled()
  })

  it('does not re-add a removed whitelist user when hydration finishes late', async () => {
    const cfg = baseConfig()
    cfg.whitelist_user_ids = [7, 42]
    cfg.forced_whitelist_user_ids = [7]
    getConfig.mockResolvedValue(cfg)

    let resolveUser42: (value: unknown) => void = () => {}
    getUserById.mockImplementation((id: number) => {
      if (id === 42) {
        return new Promise((resolve) => {
          resolveUser42 = resolve
        })
      }
      return Promise.resolve({
        id,
        email: 'admin@example.com',
        username: 'admin',
        role: 'admin',
        status: 'active',
      })
    })

    const wrapper = mountRiskControlView()
    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.scope').trigger('click')

    expect(wrapper.text()).toContain('UID 42')
    await wrapper.get('[title="admin.riskControl.removeWhitelistUser"]').trigger('click')
    expect(wrapper.text()).not.toContain('UID 42')

    resolveUser42({
      id: 42,
      email: 'late@example.com',
      username: 'late-user',
      role: 'user',
      status: 'active',
    })
    await flushPromises()
    expect(wrapper.text()).not.toContain('late@example.com')
    expect(wrapper.text()).not.toContain('UID 42')

    await findButtonByText(wrapper, 'admin.riskControl.saveConfig').trigger('click')
    await flushPromises()
    expect(updateConfig).toHaveBeenCalledWith(expect.objectContaining({
      whitelist_user_ids: [7],
    }))
  })

  it('ignores stale whitelist user search responses', async () => {
    vi.useFakeTimers()
    const pending: Record<string, (value: unknown) => void> = {}
    listUsers.mockImplementation((_page: number, _pageSize: number, filters: { search?: string }) => (
      new Promise((resolve) => {
        pending[filters.search ?? ''] = resolve
      })
    ))

    const wrapper = mountRiskControlView()
    await flushPromises()
    await findButtonByText(wrapper, 'admin.riskControl.openSettings').trigger('click')
    await findButtonByText(wrapper, 'admin.riskControl.tabs.scope').trigger('click')

    const search = wrapper.get('[data-test="whitelist-user-search"]')
    await search.setValue('old')
    vi.advanceTimersByTime(300)
    await flushPromises()
    await search.setValue('new')
    vi.advanceTimersByTime(300)
    await flushPromises()

    pending.new({
      items: [{ id: 43, email: 'new@example.com', username: 'new-user', role: 'user', status: 'active' }],
      total: 1,
      page: 1,
      page_size: 10,
      pages: 1,
    })
    await flushPromises()
    expect(wrapper.text()).toContain('new@example.com')

    pending.old({
      items: [{ id: 44, email: 'old@example.com', username: 'old-user', role: 'user', status: 'active' }],
      total: 1,
      page: 1,
      page_size: 10,
      pages: 1,
    })
    await flushPromises()
    vi.useRealTimers()

    expect(wrapper.text()).toContain('new@example.com')
    expect(wrapper.text()).not.toContain('old@example.com')
  })

  it('describes worker runtime as async audit and pre-block record processing', async () => {
    getStatus.mockResolvedValue({
      ...runtimeStatus(),
      mode: 'observe',
      processed: 12,
      queue_length: 2,
    })

    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
        },
      },
    })

    await flushPromises()

    expect(wrapper.text()).toContain('admin.riskControl.workerStatusHint')
    expect(wrapper.text()).not.toContain('admin.riskControl.preBlockSyncStatus')
    expect(wrapper.text()).toContain('admin.riskControl.records')
    expect(wrapper.text()).toContain('12')
    expect(wrapper.text()).toContain('2 / 32,768')
  })

  it('shows pre-block synchronous moderation metrics separately from worker queue', async () => {
    getStatus.mockResolvedValue({
      ...runtimeStatus(),
      pre_block_active: 2,
      pre_block_checked: 128,
      pre_block_allowed: 120,
      pre_block_blocked: 8,
      pre_block_errors: 1,
      pre_block_avg_latency_ms: 86,
      pre_block_api_key_active: 2,
      pre_block_api_key_available_count: 2,
      pre_block_api_key_total_calls: 128,
      active_workers: 3,
      worker_count: 7,
      pre_block_api_key_loads: [
        {
          index: 0,
          key_hash: 'hash-one',
          masked: 'sk-...one',
          status: 'ok',
          active: 1,
          total: 72,
          success: 70,
          errors: 2,
          avg_latency_ms: 84,
          last_latency_ms: 80,
          last_http_status: 200,
        },
        {
          index: 1,
          key_hash: 'hash-two',
          masked: 'sk-...two',
          status: 'ok',
          active: 1,
          total: 56,
          success: 56,
          errors: 0,
          avg_latency_ms: 90,
          last_latency_ms: 92,
          last_http_status: 200,
        },
      ],
    })

    const wrapper = mount(RiskControlView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          BaseDialog: BaseDialogStub,
          Icon: true,
          Select: true,
          Toggle: true,
          Pagination: true,
          ModelWhitelistSelector: ModelWhitelistSelectorStub,
        },
      },
    })

    await flushPromises()

    expect(wrapper.text()).toContain('admin.riskControl.preBlockSyncStatus')
    expect(wrapper.text()).toContain('admin.riskControl.preBlockSyncHint')
    expect(wrapper.text()).not.toContain('admin.riskControl.workerStatus')
    expect(wrapper.text()).toContain('admin.riskControl.records')
    expect(wrapper.text()).toContain('128')
    expect(wrapper.text()).toContain('120')
    expect(wrapper.text()).toContain('8')
    expect(wrapper.text()).toContain('86 ms')
    expect(wrapper.text()).toContain('admin.riskControl.preBlockAPIKeyLoad')
    expect(wrapper.text()).toContain('sk-...one')
    expect(wrapper.text()).toContain('sk-...two')
    expect(wrapper.text()).toContain('72')
    expect(wrapper.text()).toContain('56')
    expect(wrapper.text()).toContain('同步并发 2 / 可用 Key 2，累计 128 次，worker：3 / 7')

    const runtimeCards = wrapper.get('[data-test="pre-block-runtime-cards"]')
    const syncCard = wrapper.get('[data-test="pre-block-sync-card"]')
    const apiKeyLoadCard = wrapper.get('[data-test="pre-block-api-key-load-card"]')

    expect(runtimeCards.classes()).toEqual(expect.arrayContaining([
      'grid',
      'grid-cols-1',
      'xl:grid-cols-[minmax(0,520px)_minmax(0,1fr)]',
    ]))
    expect(syncCard.element.parentElement).toBe(runtimeCards.element)
    expect(apiKeyLoadCard.element.parentElement).toBe(runtimeCards.element)
    expect(syncCard.classes()).toContain('card')
    expect(apiKeyLoadCard.classes()).toContain('card')
    expect(syncCard.get('h2').text()).toBe('admin.riskControl.preBlockSyncStatus')
    expect(syncCard.text()).toContain('admin.riskControl.preBlockSyncHint')
    expect(apiKeyLoadCard.get('h2').text()).toBe('admin.riskControl.preBlockAPIKeyLoad')
    expect(apiKeyLoadCard.text()).toContain('admin.riskControl.preBlockAPIKeyLoadHint')
    expect(wrapper.get('[data-test="pre-block-api-key-load-list"]').classes()).toEqual(expect.arrayContaining([
      'max-h-[280px]',
      'overflow-y-auto',
    ]))
  })
})
