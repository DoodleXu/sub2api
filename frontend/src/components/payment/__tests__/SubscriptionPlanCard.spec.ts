import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import SubscriptionPlanCard from '../SubscriptionPlanCard.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

describe('SubscriptionPlanCard', () => {
  it('renders subscription price with ¥ while keeping quota limits in $', () => {
    const wrapper = mount(SubscriptionPlanCard, {
      props: {
        plan: {
          id: 1,
          group_id: 2,
          name: '标准订阅',
          description: '',
          price: 128,
          original_price: 168,
          validity_days: 30,
          validity_unit: 'day',
          rate_multiplier: 1,
          daily_limit_usd: 100,
          weekly_limit_usd: 200,
          monthly_limit_usd: null,
          features: [],
          group_platform: 'openai',
          sort_order: 1,
          for_sale: true,
        },
      },
    })

    expect(wrapper.text()).toContain('¥128')
    expect(wrapper.text()).toContain('¥168')
    expect(wrapper.text()).toContain('$100')
    expect(wrapper.text()).toContain('$200')
  })
})
