import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import OperationsCenterView from '../OperationsCenterView.vue'

const {
  getSettings,
  getDailyCheckinStats,
  listDailyCheckinRecords,
  updateDailyCheckinSettings,
  showError,
  showSuccess,
} = vi.hoisted(() => ({
  getSettings: vi.fn(),
  getDailyCheckinStats: vi.fn(),
  listDailyCheckinRecords: vi.fn(),
  updateDailyCheckinSettings: vi.fn(),
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
    getDailyCheckinStats,
    listDailyCheckinRecords,
    updateDailyCheckinSettings,
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

function mountView() {
  return mount(OperationsCenterView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        Toggle: ToggleStub,
        Select: SelectStub,
        Icon: true,
      },
    },
  })
}

describe('OperationsCenterView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getSettings.mockResolvedValue({ ...baseSettingsResponse })
    getDailyCheckinStats.mockResolvedValue({ ...baseStatsResponse })
    listDailyCheckinRecords.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20 })
    updateDailyCheckinSettings.mockResolvedValue({ ...baseSettingsResponse })
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
})
