import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import AdminPaymentPlansView from '../AdminPaymentPlansView.vue'

const { getPlans, getAllGroups, showError } = vi.hoisted(() => ({
  getPlans: vi.fn(),
  getAllGroups: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/admin/payment', () => ({
  adminPaymentAPI: {
    getPlans,
    updatePlan: vi.fn(),
    deletePlan: vi.fn(),
  },
}))

vi.mock('@/api/admin', () => ({
  default: {
    groups: {
      getAll: getAllGroups,
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

const DataTableStub = defineComponent({
  props: ['columns', 'data', 'loading'],
  template: `
    <table>
      <tbody>
        <tr v-for="row in data" :key="row.id">
          <td v-for="column in columns" :key="column.key">
            <slot :name="'cell-' + column.key" :row="row" :value="row[column.key]">
              {{ row[column.key] }}
            </slot>
          </td>
        </tr>
      </tbody>
    </table>
  `,
})

describe('AdminPaymentPlansView', () => {
  beforeEach(() => {
    getPlans.mockReset()
    getAllGroups.mockReset()
    showError.mockReset()
    getAllGroups.mockResolvedValue([
      {
        id: 3,
        name: 'OpenAI',
        platform: 'openai',
        rate_multiplier: 1,
      },
    ])
  })

  it('normalizes plural validity_unit values in the admin plans table', async () => {
    getPlans.mockResolvedValue({
      data: [
        {
          id: 7,
          group_id: 3,
          name: 'Weekly',
          description: '',
          price: 128,
          original_price: 0,
          validity_days: 1,
          validity_unit: 'weeks',
          features: '',
          for_sale: true,
          sort_order: 1,
        },
      ],
    })

    const wrapper = mount(AdminPaymentPlansView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          DataTable: DataTableStub,
          ConfirmDialog: true,
          GroupBadge: true,
          Icon: true,
          PlanEditDialog: true,
        },
      },
    })

    await flushPromises()

    expect(wrapper.text()).toContain('1 payment.admin.week')
    expect(wrapper.text()).not.toContain('1 payment.admin.weeks')
  })
})
