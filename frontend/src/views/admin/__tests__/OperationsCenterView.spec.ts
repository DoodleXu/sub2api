import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import OperationsCenterView from '../OperationsCenterView.vue'

const {
  getSettings,
  getOperationsOverview,
  getDailyCheckinAnalytics,
  getDailyCheckinStats,
  listDailyCheckinRecords,
  updateDailyCheckinSettings,
  exportOperationsData,
  showError,
  showSuccess,
} = vi.hoisted(() => ({
  getSettings: vi.fn(),
  getOperationsOverview: vi.fn(),
  getDailyCheckinAnalytics: vi.fn(),
  getDailyCheckinStats: vi.fn(),
  listDailyCheckinRecords: vi.fn(),
  updateDailyCheckinSettings: vi.fn(),
  exportOperationsData: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin/settings', () => ({
  default: {
    getSettings,
  },
}))

vi.mock('@/api/admin/operations', () => ({
  default: {
    getOperationsOverview,
    getDailyCheckinAnalytics,
    getDailyCheckinStats,
    listDailyCheckinRecords,
    updateDailyCheckinSettings,
    exportOperationsData,
  },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

const AppLayoutStub = { template: '<div><slot /></div>' }

const ToggleStub = defineComponent({
  props: {
    modelValue: {
      type: Boolean,
      default: false,
    },
  },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    return () =>
      h('input', {
        type: 'checkbox',
        checked: props.modelValue,
        onChange: (event: Event) => emit('update:modelValue', (event.target as HTMLInputElement).checked),
      })
  },
})

const SelectStub = defineComponent({
  props: {
    modelValue: {
      type: [String, Number, Boolean, null],
      default: '',
    },
    options: {
      type: Array,
      default: () => [],
    },
  },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    return () =>
      h(
        'select',
        {
          value: props.modelValue ?? '',
          onChange: (event: Event) => emit('update:modelValue', (event.target as HTMLSelectElement).value),
        },
        (props.options as Array<Record<string, unknown>>).map((option) =>
          h('option', { value: option.value as string }, String(option.label ?? '')),
        ),
      )
  },
})

const baseSettingsResponse = {
  daily_checkin_enabled: true,
  daily_checkin_required_usage_usd: 1,
  daily_checkin_usage_scope: 'actual_cost',
  daily_checkin_reward_min_usd: 1,
  daily_checkin_reward_max_usd: 3,
  daily_checkin_daily_budget_usd: 0,
  daily_checkin_monthly_budget_usd: 0,
  daily_checkin_user_monthly_limit_usd: 0,
  daily_checkin_budget_fallback_reward_usd: 0.01,
  daily_checkin_budget_fallback_message: '今日签到预算已用完哦～奖励0.01',
  daily_checkin_reward_tiers: [{ min_usd: 1, max_usd: 3, probability_percent: 100 }],
  daily_checkin_streak_multiplier_enabled: false,
  daily_checkin_streak_multiplier_scope: 'cross_month',
  daily_checkin_streak_multipliers: [],
  daily_checkin_crit_enabled: false,
  daily_checkin_crit_probability_percent: 0,
  daily_checkin_crit_multiplier: 1,
  daily_checkin_crit_max_reward_usd: 0,
}

const baseStatsResponse = {
  today_checkins: 0,
  today_reward_usd: 0,
  month_checkins: 0,
  month_reward_usd: 0,
  average_reward_usd: 0,
  daily_budget_usd: 0,
  daily_remaining_usd: 0,
  monthly_budget_usd: 0,
  monthly_remaining_usd: 0,
  user_monthly_limit_usd: 0,
}

const baseOverviewResponse = {
  summary: {
    dau: 2,
    new_users: 1,
    request_users: 2,
    requests: 8,
    actual_cost: 3.5,
  },
  points: [
    { date: '2026-06-15', dau: 2, new_users: 1, request_users: 2, requests: 8, actual_cost: 3.5 },
  ],
}

const baseAnalyticsResponse = {
  summary: {
    qualified_users: 2,
    checkin_users: 1,
    streak_users: 0,
    checkin_rate: 0.5,
    reward_usd: 1,
    avg_reward_usd: 1,
    fallback_rate: 0,
    crit_rate: 0,
    streak_user_rate: 0,
    daily_remaining_usd: 0,
    monthly_remaining_usd: 0,
    projected_budget_days: null,
  },
  points: [
    {
      date: '2026-06-15',
      qualified_users: 2,
      checkin_users: 1,
      checkin_rate: 0.5,
      reward_usd: 1,
      avg_reward_usd: 1,
      fallback_count: 0,
      crit_count: 0,
      streak_user_count: 0,
    },
  ],
  reward_distribution: [],
}

function mountView() {
  return mount(OperationsCenterView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        Toggle: ToggleStub,
        Select: SelectStub,
        Icon: true,
        OperationsOverviewTrendChart: true,
        DailyCheckinTrendChart: true,
      },
    },
  })
}

describe('OperationsCenterView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getSettings.mockResolvedValue({ ...baseSettingsResponse })
    getOperationsOverview.mockResolvedValue({ ...baseOverviewResponse })
    getDailyCheckinAnalytics.mockResolvedValue({ ...baseAnalyticsResponse })
    getDailyCheckinStats.mockResolvedValue({ ...baseStatsResponse })
    listDailyCheckinRecords.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20 })
    updateDailyCheckinSettings.mockResolvedValue({ ...baseSettingsResponse })
    exportOperationsData.mockResolvedValue(new Blob(['date,dau\n2026-06-15,2\n'], { type: 'text/csv' }))
  })

  it('preserves decimal reward tier amounts when saving', async () => {
    const wrapper = mountView()

    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'admin.operations.rules')?.trigger('click')

    await wrapper.get('input[placeholder="admin.operations.minReward"]').setValue('0.5')
    await wrapper.get('input[placeholder="admin.operations.maxReward"]').setValue('0.75')
    await wrapper.findAll('button').find((button) => button.text() === 'admin.operations.save')?.trigger('click')
    await flushPromises()

    expect(updateDailyCheckinSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        daily_checkin_reward_min_usd: 0.5,
        daily_checkin_reward_max_usd: 0.75,
        daily_checkin_budget_fallback_reward_usd: 0.01,
        daily_checkin_budget_fallback_message: '今日签到预算已用完哦～奖励0.01',
        daily_checkin_reward_tiers: [{ min_usd: 0.5, max_usd: 0.75, probability_percent: 100 }],
      }),
    )
  })

  it('reloads overview, analytics, and records when the date range changes', async () => {
    const wrapper = mountView()

    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'admin.operations.last7Days')?.trigger('click')
    await flushPromises()

    expect(getOperationsOverview).toHaveBeenCalledTimes(2)
    expect(getDailyCheckinAnalytics).toHaveBeenCalledTimes(2)
    expect(listDailyCheckinRecords).toHaveBeenCalledTimes(2)
    expect(getOperationsOverview).toHaveBeenLastCalledWith(expect.objectContaining({ start_date: expect.any(String), end_date: expect.any(String), timezone: expect.any(String) }))
    expect(listDailyCheckinRecords).toHaveBeenLastCalledWith(expect.objectContaining({ start_date: expect.any(String), end_date: expect.any(String), timezone: expect.any(String) }))
  })

  it('uses the same record query for listing and exporting check-in records', async () => {
    Object.defineProperty(URL, 'createObjectURL', { value: vi.fn(), configurable: true })
    Object.defineProperty(URL, 'revokeObjectURL', { value: vi.fn(), configurable: true })
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:records')
    const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    let downloadedFilename = ''
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      downloadedFilename = this.download
    })

    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'admin.operations.records')?.trigger('click')
    await wrapper.get('input[aria-label="admin.operations.dateFrom"]').setValue('2026-06-10')
    await wrapper.get('input[aria-label="admin.operations.dateTo"]').setValue('2026-06-12')
    await wrapper.findAll('button').find((button) => button.text() === 'admin.operations.filters')?.trigger('click')
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'admin.operations.exportRecords')?.trigger('click')
    await flushPromises()

    expect(listDailyCheckinRecords).toHaveBeenLastCalledWith(expect.objectContaining({
      date_from: '2026-06-10',
      date_to: '2026-06-12',
      start_date: expect.any(String),
      end_date: expect.any(String),
      timezone: expect.any(String),
    }))
    expect(exportOperationsData).toHaveBeenCalledWith(expect.objectContaining({
      dataset: 'daily_checkin_records',
      date_from: '2026-06-10',
      date_to: '2026-06-12',
      start_date: expect.any(String),
      end_date: expect.any(String),
      timezone: expect.any(String),
    }))
    expect(click).toHaveBeenCalled()
    expect(downloadedFilename).toBe('daily_checkin_records_2026-06-10_to_2026-06-12.csv')

    click.mockRestore()
    createObjectURL.mockRestore()
    revokeObjectURL.mockRestore()
  })

  it('exports the current overview dataset', async () => {
    Object.defineProperty(URL, 'createObjectURL', { value: vi.fn(), configurable: true })
    Object.defineProperty(URL, 'revokeObjectURL', { value: vi.fn(), configurable: true })
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:operations')
    const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text() === 'admin.operations.exportOverview')?.trigger('click')
    await flushPromises()

    expect(exportOperationsData).toHaveBeenCalledWith(expect.objectContaining({ dataset: 'overview_daily' }))
    expect(click).toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalledWith('admin.operations.exportSuccess')

    click.mockRestore()
    createObjectURL.mockRestore()
    revokeObjectURL.mockRestore()
  })
})
