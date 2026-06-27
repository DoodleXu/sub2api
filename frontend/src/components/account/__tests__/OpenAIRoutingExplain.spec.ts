import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import OpenAIRoutingExplainModal from '../OpenAIRoutingExplainModal.vue'
import RoutingPriorityBadge from '../RoutingPriorityBadge.vue'
import type { OpenAIRoutingAccountExplain, OpenAIRoutingSummary } from '@/types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const messages: Record<string, string> = {
    'admin.accounts.routingPriority.status.skipped': '不可调度',
    'admin.accounts.routingPriority.summary.strict_priority_lower_tier': '低于最高可用优先级层',
    'admin.accounts.routingPriority.strict.badge': 'P{priority}',
    'admin.accounts.routingPriority.strict.currentPriority': '当前最高可用优先级层：P{priority}',
    'admin.accounts.routingPriority.strict.candidateCount': '基础可调度候选：{count} 个',
    'admin.accounts.routingPriority.strict.excludedReason': '优先级 P{priority} 低于当前最高可用层 P{current}，本轮被整层排除',
    'admin.accounts.routingPriority.strict.priority': 'P{priority}',
  }
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, values?: Record<string, unknown>) => {
        let text = messages[key] ?? key
        for (const [name, value] of Object.entries(values ?? {})) {
          text = text.replace(new RegExp(`\\{${name}\\}`, 'g'), String(value))
        }
        return text
      },
      te: (key: string) => Object.prototype.hasOwnProperty.call(messages, key),
    })
  }
})

function makeSummary(overrides: Partial<OpenAIRoutingSummary> = {}): OpenAIRoutingSummary {
  return {
    account_id: 9001,
    account_name: 'openai-a',
    rank: 1,
    priority: 5,
    quality_score: 82,
    quality_grade: 'A',
    tier: 'primary',
    score: {
      total: 0.82,
      quality: 0.81,
      price: 0.79,
      latency: 0.75,
      error_rate: 0.98,
      priority: 0.16,
      load: 1,
      queue: 1,
    },
    status_label: 'candidate',
    summary_reason: 'balanced',
    summary_reasons: ['balanced'],
    is_schedulable_now: true,
    block_reasons: [],
    snapshot_at: '2026-06-27T00:00:00Z',
    ...overrides,
  }
}

describe('OpenAI routing strict priority explanation', () => {
  it('shows strict priority lower-tier state in the accounts-list badge', () => {
    const wrapper = mount(RoutingPriorityBadge, {
      props: {
        summary: makeSummary({
          rank: undefined,
          priority: 20,
          status_label: 'skipped',
          summary_reason: 'strict_priority_lower_tier',
          is_schedulable_now: false,
          block_reasons: ['strict_priority_lower_tier'],
        })
      }
    })

    expect(wrapper.text()).toContain('不可调度')
    expect(wrapper.text()).toContain('P20')
    expect(wrapper.text()).toContain('低于最高可用优先级层')
  })

  it('renders current strict priority layer and excluded accounts in the explain modal', () => {
    const explain: OpenAIRoutingAccountExplain = {
      account: makeSummary(),
      top: [makeSummary()],
      notes: ['strict_priority'],
      scheduler_strategy: 'strict_priority',
      strict_priority: {
        enabled: true,
        current_available_priority: 5,
        candidate_count: 2,
        excluded_accounts: [
          {
            account_id: 9002,
            account_name: 'openai-lower',
            priority: 20,
            current_priority: 5,
            reasons: ['strict_priority_lower_tier'],
          }
        ],
      },
    }

    const wrapper = mount(OpenAIRoutingExplainModal, {
      props: {
        show: true,
        explain,
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<div><slot /><slot name="footer" /></div>',
          },
        },
      },
    })

    expect(wrapper.text()).toContain('当前最高可用优先级层：P5')
    expect(wrapper.text()).toContain('基础可调度候选：2 个')
    expect(wrapper.text()).toContain('openai-lower')
    expect(wrapper.text()).toContain('优先级 P20 低于当前最高可用层 P5')
  })
})
