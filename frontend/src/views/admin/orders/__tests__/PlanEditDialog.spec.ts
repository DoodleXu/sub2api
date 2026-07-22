import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import PlanEditDialog from '../PlanEditDialog.vue'
import type { SubscriptionPlan } from '@/types/payment'

const paymentMocks = vi.hoisted(() => ({
  createPlan: vi.fn(),
  updatePlan: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => {
      if (key === 'payment.admin.subscriptionCnyPayPreview') return `preview ${params?.amount}`
      if (key === 'payment.admin.subscriptionCnyPayPreviewWithFee') return `fee ${params?.feeRate} ${params?.total}`
      return key
    },
  }),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
  }),
}))

vi.mock('@/api/admin/payment', () => ({
  adminPaymentAPI: {
    createPlan: paymentMocks.createPlan,
    updatePlan: paymentMocks.updatePlan,
  },
}))

function mountDialog(paymentConfig: Record<string, unknown> | null, plan: SubscriptionPlan | null = null, show = true) {
  return mount(PlanEditDialog, {
    props: {
      show,
      plan,
      groups: [],
      paymentConfig,
    },
    global: {
      stubs: {
        BaseDialog: {
          props: ['show'],
          template: '<div v-if="show"><slot /><slot name="footer" /></div>',
        },
        Select: true,
        Icon: true,
        GroupBadge: true,
      },
    },
  })
}

describe('PlanEditDialog subscription CNY payment preview', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('shows CNY channel charge using the configured subscription rate and fee', async () => {
    const wrapper = mountDialog({
      subscription_usd_to_cny_rate: 7.15,
      recharge_fee_rate: 2.5,
    })

    await wrapper.find('input[type="number"]').setValue('9.99')

    expect(wrapper.text()).toContain('preview')
    expect(wrapper.text()).toContain('¥71.43')
    expect(wrapper.text()).toContain('fee 2.5')
    expect(wrapper.text()).toContain('¥73.22')
  })

  it('hides the preview when the subscription rate is not configured', async () => {
    const wrapper = mountDialog({
      subscription_usd_to_cny_rate: 0,
      recharge_fee_rate: 2.5,
    })

    await wrapper.find('input[type="number"]').setValue('9.99')

    expect(wrapper.text()).not.toContain('preview')
    expect(wrapper.text()).not.toContain('¥71.43')
  })

  it('backfills an empty legacy currency and submits an uppercase currency', async () => {
    const plan = {
      id: 7,
      name: 'Legacy plan',
      description: 'Legacy plan',
      group_id: 1,
      price: 9.99,
      original_price: 0,
      currency: '',
      validity_days: 30,
      validity_unit: 'days',
      sort_order: 0,
      for_sale: true,
      features: [],
    } as SubscriptionPlan
    const wrapper = mountDialog(null, plan, false)

    await wrapper.setProps({ show: true })
    const currencyInput = wrapper.find('input[maxlength="3"]')
    expect((currencyInput.element as HTMLInputElement).value).toBe('CNY')

    await currencyInput.setValue('usd')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(paymentMocks.updatePlan).toHaveBeenCalledWith(
      7,
      expect.objectContaining({ currency: 'USD' }),
    )
  })
})
