import { defineComponent, h } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import OrderTable from '../OrderTable.vue'
import type { PaymentOrder } from '@/types/payment'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (key === 'payment.orders.creditDays') return `${params?.days} day(s)`
        return key
      },
    }),
  }
})

const DataTableStub = defineComponent({
  name: 'DataTable',
  props: {
    columns: { type: Array, required: true },
    data: { type: Array, required: true },
  },
  setup(props, { slots }) {
    return () => {
      const columns = props.columns as Array<{ key: string; label: string }>
      const rows = props.data as PaymentOrder[]
      const headerCells = columns.map((column) => h('th', { key: column.key }, column.label))
      const bodyRows = rows.map((row) => {
        const cells = columns.map((column) => {
          const slot = slots[`cell-${column.key}`]
          return h('td', { key: column.key }, slot
            ? slot({ row, value: row[column.key as keyof PaymentOrder] })
            : String(row[column.key as keyof PaymentOrder] ?? ''))
        })
        return h('tr', { key: row.id }, cells)
      })

      return h('table', [
        h('thead', h('tr', headerCells)),
        h('tbody', bodyRows),
      ])
    }
  },
})

function order(overrides: Partial<PaymentOrder> = {}): PaymentOrder {
  return {
    id: 1,
    user_id: 10,
    amount: 80,
    pay_amount: 80,
    fee_rate: 0,
    payment_type: 'wxpay',
    out_trade_no: 'sub2_order_1',
    status: 'COMPLETED',
    order_type: 'subscription',
    created_at: '2026-05-17T00:00:00Z',
    expires_at: '2026-05-17T00:10:00Z',
    refund_amount: 0,
    ...overrides,
  }
}

describe('OrderTable', () => {
  it('renders subscription upgrade credit column for admin orders', () => {
    const wrapper = mount(OrderTable, {
      props: {
        orders: [order({
          upgrade_from_subscription_id: 55,
          upgrade_credit_amount: 20,
          upgrade_credit_days: 12,
        })],
        loading: false,
        showUpgradeCredit: true,
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          OrderStatusBadge: defineComponent({
            name: 'OrderStatusBadge',
            props: { status: { type: String, required: true } },
            setup(props) {
              return () => h('span', props.status)
            },
          }),
        },
      },
    })

    expect(wrapper.text()).toContain('payment.orders.subscriptionCredit')
    expect(wrapper.text()).toContain('-¥20.00')
    expect(wrapper.text()).toContain('12 day(s)')
    expect(wrapper.text()).toContain('#55')
  })

  it('keeps upgrade credit column hidden in user order tables', () => {
    const wrapper = mount(OrderTable, {
      props: {
        orders: [order({ upgrade_credit_amount: 20 })],
        loading: false,
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          OrderStatusBadge: true,
        },
      },
    })

    expect(wrapper.text()).not.toContain('payment.orders.subscriptionCredit')
    expect(wrapper.text()).not.toContain('-¥20.00')
  })
})
