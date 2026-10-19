import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountQualityPanel from '../AccountQualityPanel.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('AccountQualityPanel', () => {
  it('区分未知质量与零错误率，并提示欠费须手动恢复', () => {
    const wrapper = mount(AccountQualityPanel, {
      props: {
        quality: {
          strategy: 'health_first',
          auto_pause: { reason: 'insufficient_account_balance', message: 'Insufficient account balance', at: '2026-10-19T08:00:00Z' },
          models: [{ model: 'model-a', capability: 'generation', transport: 'http', effort: 'high', stream: true, state: 'unknown', samples: 3, latency_samples: 2, updated_at: 1792387200000, window: '24h' }]
        }
      }
    })
    expect(wrapper.text()).toContain('admin.accounts.quality.healthFirst')
    expect(wrapper.text()).toContain('admin.accounts.quality.unknown')
    expect(wrapper.text()).toContain('admin.accounts.quality.noLatency')
    expect(wrapper.text()).toContain('admin.accounts.quality.manualResume')
    expect(wrapper.text()).not.toContain('0.0%')
    expect(wrapper.text()).toContain('model-a')
  })
})
