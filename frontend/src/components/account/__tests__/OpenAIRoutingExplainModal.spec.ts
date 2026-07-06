import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import OpenAIRoutingExplainModal from '../OpenAIRoutingExplainModal.vue'
import type { OpenAIRoutingAccountExplain } from '@/types'

const messages: Record<string, string> = {
  'admin.accounts.routingPriority.modal.title': 'OpenAI 调度解释',
  'admin.accounts.routingPriority.modal.experimentalTitle': 'OpenAI 试验性调度解释',
  'admin.accounts.routingPriority.modal.strictTitle': 'OpenAI Strict Priority 调度解释',
  'admin.accounts.routingPriority.sections.score': '分项评分',
  'admin.accounts.routingPriority.sections.selectionBasis': '选择依据',
  'admin.accounts.routingPriority.sections.strictPriority': 'Strict Priority 排障',
  'admin.accounts.routingPriority.sections.notes': '说明',
  'admin.accounts.routingPriority.sections.blockReasons': '不可调度原因',
  'admin.accounts.routingPriority.sections.topCandidates': 'Top 候选',
  'admin.accounts.routingPriority.sections.priceSource': '价格来源',
  'admin.accounts.routingPriority.score.total': '综合分',
  'admin.accounts.routingPriority.score.quality': '质量',
  'admin.accounts.routingPriority.score.price': '价格',
  'admin.accounts.routingPriority.score.latency': '响应',
  'admin.accounts.routingPriority.score.error_rate': '错误率',
  'admin.accounts.routingPriority.score.priority': '优先级',
  'admin.accounts.routingPriority.score.load': '负载',
  'admin.accounts.routingPriority.score.queue': '队列',
  'admin.accounts.routingPriority.strict.priorityLabel': '优先级层',
  'admin.accounts.routingPriority.strict.priority': 'P{priority}',
  'admin.accounts.routingPriority.strict.lastUsed': 'LastUsed',
  'admin.accounts.routingPriority.strict.neverUsed': '从未使用',
  'admin.accounts.routingPriority.strict.currentLayer': '当前层内依据',
  'admin.accounts.routingPriority.strict.currentPriority': '当前最高可用优先级层：P{priority}',
  'admin.accounts.routingPriority.strict.candidateCount': '基础可调度候选：{count} 个',
  'admin.accounts.routingPriority.notes.strict_priority': 'Strict Priority 只在当前最高可用优先级层内选择账号。',
  'admin.accounts.routingPriority.notes.strict_priority_top_tier_only': '低优先级层不会参与本轮 Top 候选，除非更高优先级层没有可用账号。',
  'admin.accounts.routingPriority.notes.strict_priority_same_tier_last_used': '同一优先级层内优先使用从未使用或最久未使用的账号；完全相同时会打散以避免热点。',
  'admin.accounts.routingPriority.notes.experimental_scheduler': '试验性调度按价格、质量、响应、错误率、优先级和负载综合排序。',
  'admin.accounts.routingPriority.notes.price_uses_upstream_effective_then_group_then_account_rate_multiplier': '价格评分按上游实时有效倍率、上游分组倍率、账号倍率依次回退。',
  'admin.accounts.routingPriority.priceSource.source': '命中来源',
  'admin.accounts.routingPriority.priceSource.rateMultiplier': '参与评分倍率',
  'admin.accounts.routingPriority.priceSource.fallback': '回退状态',
  'admin.accounts.routingPriority.priceSource.rateValue': '{rate}x',
  'admin.accounts.routingPriority.priceSource.values.upstream_effective_rate_multiplier': '上游实时有效倍率',
  'admin.accounts.routingPriority.priceSource.values.upstream_group_rate_multiplier': '上游分组倍率',
  'admin.accounts.routingPriority.priceSource.values.account.rate_multiplier': '账号倍率',
  'admin.accounts.routingPriority.priceSource.fallbackReasons.upstream_rate_missing': '缺少上游倍率，当前使用账号倍率回退；可刷新上游余额/倍率',
  'admin.accounts.routingPriority.priceSource.fallbackReasons.account_rate_default_1': '账号未配置倍率，当前按默认 1.0 回退',
  'admin.accounts.routingPriority.summary.strict_priority_top_tier': '当前最高可用优先级层',
  'admin.accounts.routingPriority.summary.experimental_circuit_open': '试验性调度熔断冷却中',
  'common.close': '关闭',
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, string | number>) => {
      const template = messages[key] ?? key
      return Object.entries(params ?? {}).reduce((text, [name, value]) => text.replaceAll(`{${name}}`, String(value)), template)
    },
    te: (key: string) => Object.prototype.hasOwnProperty.call(messages, key),
  }),
}))

const mountModal = (explain: OpenAIRoutingAccountExplain) => {
  return mount(OpenAIRoutingExplainModal, {
    props: {
      show: true,
      explain,
    },
    global: {
      stubs: {
        BaseDialog: {
          props: ['show', 'title'],
          template: '<div v-if="show"><h1 data-testid="title">{{ title }}</h1><slot /><slot name="footer" /></div>',
        },
      },
    },
  })
}

const baseExplain = (overrides: Partial<OpenAIRoutingAccountExplain> = {}): OpenAIRoutingAccountExplain => ({
  account: {
    account_id: 74062,
    account_name: 'top-never',
    rank: 1,
    priority: 0,
    last_used_at: null,
    quality_score: 95,
    quality_grade: 'S',
    tier: 'primary',
    score: {
      total: 0.91,
      quality: 0.95,
      price: 0.8,
      latency: 0.9,
      error_rate: 1,
      priority: 1,
      load: 1,
      queue: 1,
    },
    price_source: {
      source: 'upstream_effective_rate_multiplier',
      rate_multiplier: 0.08,
    },
    status_label: 'candidate',
    summary_reason: 'strict_priority_top_tier',
    summary_reasons: ['strict_priority_top_tier', 'strict_priority_never_used_first'],
    is_schedulable_now: true,
    snapshot_at: '2026-06-27T08:00:00Z',
  },
  top: [
    {
      account_id: 74062,
      account_name: 'top-never',
      rank: 1,
      priority: 0,
      last_used_at: null,
      quality_score: 95,
      quality_grade: 'S',
      tier: 'primary',
      score: {
        total: 0.91,
        quality: 0.95,
        price: 0.8,
        latency: 0.9,
        error_rate: 1,
        priority: 1,
        load: 1,
        queue: 1,
      },
      price_source: {
        source: 'upstream_effective_rate_multiplier',
        rate_multiplier: 0.08,
      },
      status_label: 'candidate',
      summary_reason: 'strict_priority_top_tier',
      summary_reasons: ['strict_priority_top_tier', 'strict_priority_never_used_first'],
      is_schedulable_now: true,
      snapshot_at: '2026-06-27T08:00:00Z',
    },
  ],
  notes: ['strict_priority', 'strict_priority_top_tier_only', 'strict_priority_same_tier_last_used'],
  scheduler_strategy: 'strict_priority',
  strict_priority: {
    enabled: true,
    current_available_priority: 0,
    candidate_count: 2,
    excluded_accounts: [],
  },
  ...overrides,
})

describe('OpenAIRoutingExplainModal', () => {
  it('shows strict priority title and top candidate basis without experimental score wording', () => {
    const wrapper = mountModal(baseExplain())

    expect(wrapper.get('[data-testid="title"]').text()).toBe('OpenAI Strict Priority 调度解释')
    expect(wrapper.text()).toContain('Strict Priority 只在当前最高可用优先级层内选择账号。')
    expect(wrapper.text()).toContain('P0 · 从未使用')
    expect(wrapper.text()).not.toContain('OpenAI 试验性调度解释')
    expect(wrapper.text()).not.toContain('综合分')
  })

  it('keeps the experimental scheduler title for experimental explains', () => {
    const wrapper = mountModal(baseExplain({
      account: {
        ...baseExplain().account,
        price_source: {
          source: 'upstream_group_rate_multiplier',
          rate_multiplier: 0.12,
        },
      },
      notes: ['experimental_scheduler', 'price_uses_upstream_effective_then_group_then_account_rate_multiplier'],
      scheduler_strategy: 'experimental_scheduler',
      strict_priority: {
        enabled: false,
        candidate_count: 0,
        excluded_accounts: [],
      },
    }))

    expect(wrapper.get('[data-testid="title"]').text()).toBe('OpenAI 试验性调度解释')
    expect(wrapper.text()).toContain('综合分')
    expect(wrapper.text()).toContain('价格来源')
    expect(wrapper.text()).toContain('上游分组倍率')
    expect(wrapper.text()).toContain('0.12x')
    expect(wrapper.text()).toContain('价格评分按上游实时有效倍率、上游分组倍率、账号倍率依次回退。')
  })

  it('marks account-rate fallback when upstream rates are unavailable', () => {
    const wrapper = mountModal(baseExplain({
      account: {
        ...baseExplain().account,
        score: {
          ...baseExplain().account.score,
          price: 0.5,
        },
        price_source: {
          source: 'account.rate_multiplier',
          rate_multiplier: 1,
          fallback: true,
          fallback_reason: 'account_rate_default_1',
        },
      },
      notes: ['experimental_scheduler', 'price_uses_upstream_effective_then_group_then_account_rate_multiplier'],
      scheduler_strategy: 'experimental_scheduler',
      strict_priority: {
        enabled: false,
        candidate_count: 0,
        excluded_accounts: [],
      },
    }))

    expect(wrapper.text()).toContain('价格来源')
    expect(wrapper.text()).toContain('账号倍率')
    expect(wrapper.text()).toContain('1x')
    expect(wrapper.text()).toContain('回退状态')
    expect(wrapper.text()).toContain('账号未配置倍率，当前按默认 1.0 回退')
  })

  it('translates experimental circuit cooldown reasons', () => {
    const wrapper = mountModal(baseExplain({
      account: {
        ...baseExplain().account,
        summary_reason: 'experimental_circuit_open',
        summary_reasons: ['experimental_circuit_open'],
        block_reasons: ['experimental_circuit_open'],
        is_schedulable_now: false,
      },
      notes: ['experimental_scheduler'],
      scheduler_strategy: 'experimental_scheduler',
      strict_priority: {
        enabled: false,
        candidate_count: 0,
        excluded_accounts: [],
      },
    }))

    expect(wrapper.text()).toContain('试验性调度熔断冷却中')
    expect(wrapper.text()).not.toContain('experimental_circuit_open')
  })
})
