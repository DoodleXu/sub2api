import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import type { Account } from '@/types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (!params) return key
        return Object.entries(params).reduce((text, [name, value]) => `${text}:${name}=${String(value)}`, key)
      }
    })
  }
})

vi.mock('@/api/admin/accounts', () => ({
  accountsAPI: {
    refreshUpstreamBalance: vi.fn()
  }
}))

import OpenAIUpstreamBalanceCell from '../OpenAIUpstreamBalanceCell.vue'

function account(overrides: Partial<Account> = {}): Account {
  return {
    id: 402,
    name: 'AI Nexus',
    platform: 'openai',
    type: 'apikey',
    extra: {},
    proxy_id: null,
    concurrency: 1,
    priority: 0,
    status: 'active',
    error_message: null,
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides
  } as Account
}

describe('OpenAIUpstreamBalanceCell', () => {
  it('hardcodes account 401 upstream balance as infinity', () => {
    const wrapper = mount(OpenAIUpstreamBalanceCell, {
      props: {
        account: account({
          id: 401,
          extra: {
            upstream_balance_status: 'error',
            upstream_balance_error: 'GET https://example.com failed',
            upstream_balance_provider: 'OpenAI'
          }
        })
      }
    })

    expect(wrapper.text()).toContain('♾️')
    expect(wrapper.text()).not.toContain('admin.accounts.upstreamBalance.failed')
    expect(wrapper.text()).not.toContain('admin.accounts.upstreamBalance.refresh')
    expect(wrapper.text()).not.toContain('admin.accounts.upstreamBalance.errorHint')
    expect(wrapper.find('button').exists()).toBe(false)
  })

  it('keeps regular accounts on the normal balance path', () => {
    const wrapper = mount(OpenAIUpstreamBalanceCell, {
      props: {
        account: account({
          extra: {
            upstream_balance_status: 'ok',
            upstream_balance_provider: 'OpenAI',
            upstream_balance_remaining: 12.34567,
            upstream_balance_unit: 'USD'
          }
        })
      }
    })

    expect(wrapper.text()).toContain('$12.3457')
    expect(wrapper.text()).toContain('OpenAI')
    expect(wrapper.text()).toContain('admin.accounts.upstreamBalance.refresh')
    expect(wrapper.find('button').exists()).toBe(true)
  })

  it('shows account-rate fallback when upstream rates are missing', () => {
    const wrapper = mount(OpenAIUpstreamBalanceCell, {
      props: {
        account: account({
          rate_multiplier: 0.2,
          extra: {
            upstream_balance_status: 'ok',
            upstream_balance_provider: 'OpenAI'
          }
        })
      }
    })

    expect(wrapper.text()).toContain('admin.accounts.upstreamBalance.accountRateFallback:rate=0.20')
    expect(wrapper.text()).toContain('admin.accounts.upstreamBalance.refresh')
  })

  it('shows upstream effective rate ahead of account-rate fallback', () => {
    const wrapper = mount(OpenAIUpstreamBalanceCell, {
      props: {
        account: account({
          rate_multiplier: 0.2,
          extra: {
            upstream_balance_status: 'ok',
            upstream_balance_provider: 'OpenAI',
            upstream_effective_rate_multiplier: 0.08,
            upstream_group_rate_multiplier: 0.12
          }
        })
      }
    })

    expect(wrapper.text()).toContain('admin.accounts.upstreamBalance.realRate:rate=0.08')
    expect(wrapper.text()).not.toContain('admin.accounts.upstreamBalance.accountRateFallback')
  })
})
