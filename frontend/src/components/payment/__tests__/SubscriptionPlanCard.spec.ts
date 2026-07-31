import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createPinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import type { SubscriptionPlan } from '@/types/payment'
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

const mountPlanCard = (overrides: Partial<SubscriptionPlan> = {}) =>
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
    global: { plugins: [i18n, createPinia()] },
  })

describe('SubscriptionPlanCard', () => {
  it('renders subscription price with ¥ while keeping quota limits in $', () => {
    const wrapper = mountPlanCard({
      name: '标准订阅',
      currency: 'CNY',
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

  it('uses the configured currency symbol while preserving CNY for legacy plans', () => {
    const cnyPlan = mountPlanCard({ currency: 'CNY', original_price: 20 }).text()

    expect(cnyPlan).toContain('¥10CNY')
    expect(cnyPlan).toContain('¥20CNY')
    expect(mountPlanCard({ currency: 'USD' }).text()).toContain('$10USD')
    expect(mountPlanCard({ currency: '' }).text()).toContain('¥10')
  })

  it.each([
    ['long Chinese', '企业全球加速专业订阅套餐（含高级模型与优先支持）'],
    ['long English', 'Enterprise Global Acceleration Subscription with Priority Support'],
    ['unbroken token', 'EnterpriseGlobalAccelerationSubscriptionWithPrioritySupport1234567890'],
  ])('keeps the full %s plan title accessible in a bounded two-line area', (_label, name) => {
    const wrapper = mountPlanCard({ name })
    const title = wrapper.get('h3')

    expect(title.text()).toBe(name)
    expect(title.attributes('title')).toBe(name)
    expect(title.classes()).toEqual(expect.arrayContaining([
      'min-w-0',
      'h-12',
      'break-words',
      'line-clamp-2',
      '[overflow-wrap:anywhere]',
    ]))
    expect(title.classes()).not.toContain('truncate')
  })

  it('keeps title, badge, price, description, and purchase action in separate bounded regions', () => {
    const wrapper = mountPlanCard({
      name: 'Enterprise Global Acceleration Subscription with Priority Support',
      price: 123.45,
      currency: 'USD',
      description: 'Includes advanced models and priority support.',
    })
    const title = wrapper.get('h3')
    const badge = wrapper.findAll('span').find((node) => node.text() === 'OpenAI')
    const price = wrapper.findAll('span').find((node) => node.text() === '123.45')

    expect(title.element.parentElement?.classList).toContain('min-w-0')
    expect(title.element.parentElement?.classList).toContain('flex-1')
    expect(badge?.classes()).toContain('shrink-0')
    expect([...(badge?.element.parentElement?.classList ?? [])]).toEqual(expect.arrayContaining([
      'flex',
      'items-center',
      'justify-end',
    ]))
    expect(badge?.element.parentElement?.textContent).toContain('/ 30payment.days')
    expect(badge?.element.parentElement?.parentElement?.classList).toContain('shrink-0')
    expect(price?.element.parentElement?.parentElement?.classList).toContain('shrink-0')
    expect(wrapper.get('p').text()).toBe('Includes advanced models and priority support.')
    expect(wrapper.get('button').text()).toBe('payment.subscribeNow')
  })

  it('keeps short plan titles compact and aligned', () => {
    const wrapper = mountPlanCard({ name: 'Pro', description: '' })
    const title = wrapper.get('h3')
    const badge = wrapper.findAll('span').find((node) => node.text() === 'OpenAI')

    expect(title.text()).toBe('Pro')
    expect(title.attributes('title')).toBe('Pro')
    expect(title.classes()).toEqual(expect.arrayContaining(['text-base', 'font-bold', 'h-12']))
    expect([...(badge?.element.parentElement?.classList ?? [])]).toEqual(expect.arrayContaining([
      'flex',
      'items-center',
      'justify-end',
    ]))
    expect(badge?.element.parentElement?.textContent).toContain('/ 30payment.days')
  })
})
