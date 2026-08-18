import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import OperationsBusinessInsights from '../OperationsBusinessInsights.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

const baseProps = {
  loading: false,
  error: false,
  stats: null,
  payment: null,
  ranking: {
    ranking: [],
    total_actual_cost: 12,
    total_requests: 4,
    total_tokens: 500,
    start_date: '2026-06-01',
    end_date: '2026-06-30',
    data_available: true,
    aggregation_complete: true,
  },
  models: [],
  groups: [],
  dataAvailable: true,
  aggregationComplete: true,
  groupDetailsAvailable: true,
  rewardCost: 1,
  retention: null,
  periodRequestUsers: 2,
  rangeStart: '2026-06-01',
  rangeEnd: '2026-06-30',
}

describe('OperationsBusinessInsights', () => {
  it('does not present uncovered aggregate values as zero or valid margin', () => {
    const wrapper = mount(OperationsBusinessInsights, {
      props: { ...baseProps, dataAvailable: false },
    })

    expect(wrapper.text()).toContain('admin.operations.businessDataUnavailable')
    expect(wrapper.text()).not.toContain('USD 12.00')
  })

  it('marks current aggregate prefixes as partial while keeping usable values', () => {
    const wrapper = mount(OperationsBusinessInsights, {
      props: { ...baseProps, aggregationComplete: false },
    })

    expect(wrapper.text()).toContain('admin.operations.businessDataPartial')
    expect(wrapper.text()).toContain('USD 12.00')
  })

  it('explains why group details are absent for long ranges', () => {
    const wrapper = mount(OperationsBusinessInsights, {
      props: { ...baseProps, groupDetailsAvailable: false },
    })

    expect(wrapper.text()).toContain('admin.operations.groupDetailsRangeLimited')
  })

  it('keeps payment currencies separate and computes contribution margin on the USD basis', () => {
    const wrapper = mount(OperationsBusinessInsights, {
      props: {
        ...baseProps,
        payment: {
          total_amount: { CNY: 10, USD: 20 },
          admin_recharge_amount: 5,
          total_count: 2,
        },
        models: [{ model: 'test-model', requests: 1, actual_cost: 8, account_cost: 8, real_cost_cny: 4 }],
      },
    })

    expect(wrapper.text()).toContain('CNY 10.00 / USD 20.00')
    expect(wrapper.text()).toContain('USD 5.00')
    expect(wrapper.text()).toContain('CNY 4.00')
    expect(wrapper.text()).toContain('USD 8.00')
    expect(wrapper.text()).toContain('USD 3.00 / 25.0%')
  })
})
