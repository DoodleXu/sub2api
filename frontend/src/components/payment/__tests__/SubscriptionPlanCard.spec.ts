import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import SubscriptionPlanCard from '../SubscriptionPlanCard.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'en',
  fallbackWarn: false,
  missingWarn: false,
  messages: {
    en: {
      payment: {
        day: 'day',
        days: 'days',
        week: 'week',
        weeks: 'weeks',
        month: 'month',
        months: 'months',
        year: 'year',
        years: 'years',
        models: 'Models',
        planCard: {
          dailyLimit: 'Daily',
          weeklyLimit: 'Weekly',
          monthlyLimit: 'Monthly',
          quota: 'Quota',
          rate: 'Rate',
          unlimited: 'Unlimited',
        },
        subscribeNow: 'Subscribe now',
      },
    },
  },
})

const mountPlanCard = (overrides: Record<string, unknown> = {}) =>
  mount(SubscriptionPlanCard, {
    props: {
      plan: {
        id: 1,
        group_id: 10,
        group_platform: 'openai',
        group_subscription_type: 'subscription',
        name: 'Pro',
        description: '',
        price: 10,
        original_price: null,
        features: [],
        rate_multiplier: 1,
        validity_days: 30,
        validity_unit: 'day',
        daily_limit_usd: null,
        weekly_limit_usd: null,
        monthly_limit_usd: null,
        supported_model_scopes: ['claude', 'gemini_text', 'gemini_image'],
        is_active: true,
        for_sale: true,
        sort_order: 1,
        ...overrides,
      },
    },
    global: { plugins: [i18n] },
  })

describe('SubscriptionPlanCard', () => {
  it('renders subscription price with ¥ while keeping quota limits in $', () => {
    const wrapper = mountPlanCard({
      name: '标准订阅',
      price: 128,
      original_price: 168,
      daily_limit_usd: 100,
      weekly_limit_usd: 200,
    })

    expect(wrapper.text()).toContain('¥128')
    expect(wrapper.text()).toContain('¥168')
    expect(wrapper.text()).toContain('$100')
    expect(wrapper.text()).toContain('$200')
  })

  it('does not show Antigravity model scopes for OpenAI plans', () => {
    const text = mountPlanCard({ group_platform: 'openai' }).text()

    expect(text).not.toContain('Claude')
    expect(text).not.toContain('Gemini')
    expect(text).not.toContain('Imagen')
  })

  it('shows model scopes for Antigravity plans', () => {
    const text = mountPlanCard({ group_platform: 'antigravity' }).text()

    expect(text).toContain('Claude')
    expect(text).toContain('Gemini')
    expect(text).toContain('Imagen')
  })

  it('renders weekly validity unit and hides monthly limit for weekly quota plans', () => {
    const text = mountPlanCard({
      validity_days: 1,
      validity_unit: 'weeks',
      group_subscription_type: 'subscription_weekly',
      weekly_limit_usd: 100,
      monthly_limit_usd: 300,
    }).text()

    expect(text).toContain('/ 1payment.week')
    expect(text).toContain('$100')
    expect(text).not.toContain('$300')
  })

  it('only shows daily limit for daily quota plans', () => {
    const text = mountPlanCard({
      group_subscription_type: 'subscription_daily',
      daily_limit_usd: 20,
      weekly_limit_usd: 100,
      monthly_limit_usd: 300,
    }).text()

    expect(text).toContain('$20')
    expect(text).not.toContain('$100')
    expect(text).not.toContain('$300')
  })
})
