import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import DailyCheckinTrendChart from '../DailyCheckinTrendChart.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('vue-chartjs', () => ({
  Chart: {
    props: ['data', 'options'],
    template: '<div class="chart-config">{{ JSON.stringify({ data, options }) }}</div>',
  },
}))

describe('DailyCheckinTrendChart', () => {
  it('leaves headroom for 100 percent check-in rate markers', () => {
    const wrapper = mount(DailyCheckinTrendChart, {
      props: {
        loading: false,
        points: [
          {
            date: '2026-06-01',
            qualified_users: 10,
            checkin_users: 10,
            checkin_rate: 1,
            reward_usd: 1,
            avg_reward_usd: 1,
            fallback_count: 0,
            crit_count: 0,
            streak_user_count: 0,
          },
        ],
      },
    })

    const config = JSON.parse(wrapper.get('.chart-config').text())
    const rateDataset = config.data.datasets.find(
      (dataset: Record<string, unknown>) => dataset.yAxisID === 'y1',
    )

    expect(rateDataset.data).toEqual([100])
    expect(rateDataset.clip).toBe(false)
    expect(config.options.layout.padding.top).toBeGreaterThan(0)
  })
})
