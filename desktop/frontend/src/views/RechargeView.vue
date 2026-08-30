<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { nativeAvailable, nativeCall, type CheckoutSession, type ConnectionSummary } from '../native'

const amount = ref(10)
const paymentType = ref('alipay')
const connection = ref<ConnectionSummary | null>(null)
const checkout = ref<CheckoutSession | null>(null)
const busy = ref(false)
const loading = ref(false)
const message = ref('')
const balanceAfterPayment = ref<number | null>(null)
const balanceUnit = ref('USD')
let pollTimer: number | undefined

function stopPolling() {
  if (pollTimer !== undefined) window.clearTimeout(pollTimer)
  pollTimer = undefined
}

function schedulePolling() {
  stopPolling()
  if (!checkout.value) return
  const delay = Math.max(3, checkout.value.poll_after_seconds || 5) * 1000
  pollTimer = window.setTimeout(() => { void pollCheckout() }, delay)
}

async function refreshBalanceAfterPayment() {
  try {
    const overview = await nativeCall((app) => app.GetUsageOverview())
    const account = overview.account
    const balance = Number(account?.balance)
    if (overview.account_ready && account && account.valid !== false && Number.isFinite(balance)) {
      balanceAfterPayment.value = balance
      balanceUnit.value = account.unit || 'USD'
      return true
    }
  } catch {
    // Payment status is authoritative; a transient balance read failure must
    // not turn a completed checkout into a failed payment.
  }
  return false
}

async function pollCheckout() {
  const current = checkout.value
  if (!current || !nativeAvailable()) return
  try {
    const next = await nativeCall((app) => app.GetCheckoutSession(current.session_id))
    checkout.value = next
    const status = next.status.toLowerCase()
    if (['paid', 'completed', 'succeeded', 'success'].includes(status)) {
      const refreshed = await refreshBalanceAfterPayment()
      message.value = refreshed
        ? `充值已完成，当前账户余额 ${balanceAfterPayment.value!.toFixed(2)} ${balanceUnit.value}`.trim()
        : '充值已完成，但余额暂时无法刷新，请稍后到用量页重试。'
      stopPolling()
      return
    }
    if (['failed', 'cancelled', 'canceled', 'expired'].includes(status)) {
      message.value = `充值状态：${next.status}`
      stopPolling()
      return
    }
    schedulePolling()
  } catch (error) {
    stopPolling()
    message.value = error instanceof Error ? error.message : '查询充值状态失败'
  }
}

async function load() {
  if (!nativeAvailable()) return
  loading.value = true
  try {
    connection.value = await nativeCall((app) => app.GetConnection())
  } catch (error) {
    message.value = error instanceof Error ? error.message : '读取连接状态失败'
  } finally {
    loading.value = false
  }
}

async function openCheckout() {
  if (!checkout.value || !nativeAvailable() || busy.value) return
  busy.value = true
  try {
    await nativeCall((app) => app.OpenCheckout(checkout.value!.session_id))
    message.value = '已在浏览器打开官方托管支付页。二维码、跳转和第三方支付均由官方页面处理。'
  } catch (error) {
    message.value = error instanceof Error ? error.message : '打开支付页失败'
  } finally {
    busy.value = false
  }
}

async function createCheckout() {
  if (!nativeAvailable()) {
    message.value = '请在桌面应用中运行充值。'
    return
  }
  if (!connection.value?.session_configured) {
    message.value = '请先在客户端配置中完成官方设备授权。'
    return
  }
  if (!Number.isFinite(amount.value) || amount.value <= 0) {
    message.value = '请输入大于 0 的充值金额。'
    return
  }
  busy.value = true
  message.value = ''
  balanceAfterPayment.value = null
  stopPolling()
  try {
    const created = await nativeCall((app) => app.CreateCheckoutSession({
      amount: amount.value,
      payment_type: paymentType.value,
    }))
    checkout.value = created
    await nativeCall((app) => app.OpenCheckout(created.session_id))
    message.value = '已创建充值会话并打开官方托管支付页。'
    schedulePolling()
  } catch (error) {
    checkout.value = null
    message.value = error instanceof Error ? error.message : '创建充值会话失败'
  } finally {
    busy.value = false
  }
}

onMounted(load)
onBeforeUnmount(stopPolling)
</script>

<template>
  <div class="max-w-3xl space-y-8">
    <div>
      <p class="max-w-2xl text-sm leading-6 text-muted">充值只创建短时、一次性的官方 checkout 会话。provider secret 不会进入桌面配置或 URL，支付步骤在浏览器托管页面完成。</p>
      <p v-if="connection?.session_configured" class="mt-4 text-xs text-teal-200"><span class="mr-2 inline-block h-2 w-2 rounded-full bg-teal-300" />设备会话已连接</p>
      <p v-else-if="!loading" class="mt-4 text-xs text-amber-200">尚未连接设备会话，请先完成客户端配置。</p>
    </div>

    <section class="border-t border-white/10 pt-5">
      <div class="grid gap-4 sm:grid-cols-2">
        <label class="block text-sm text-ink">金额<input v-model.number="amount" class="field" type="number" min="0.01" step="0.01" inputmode="decimal" /></label>
        <label class="block text-sm text-ink">支付方式<select v-model="paymentType" class="field"><option value="alipay">支付宝</option><option value="wxpay">微信支付</option><option value="stripe">Stripe</option><option value="airwallex">Airwallex</option></select></label>
      </div>
      <div class="mt-5 flex flex-wrap gap-3">
        <button class="action-primary" :disabled="busy || loading || !connection?.session_configured" type="button" @click="createCheckout">{{ busy ? '准备中…' : '创建并打开支付页' }}</button>
        <button v-if="checkout" class="action-secondary" :disabled="busy" type="button" @click="openCheckout">重新打开</button>
      </div>
    </section>

    <section v-if="checkout" class="border-t border-white/[.08] pt-5">
      <p class="text-xs uppercase tracking-[.18em] text-muted">当前会话</p>
      <p class="mt-3 font-mono text-sm text-white">{{ checkout.session_id }}</p>
      <p class="mt-2 text-sm text-muted">状态：{{ checkout.status }}<span v-if="checkout.pay_amount"> · 应付 {{ checkout.pay_amount }} {{ checkout.currency || '' }}</span></p>
      <p class="mt-2 text-xs leading-5 text-muted">客户端会按服务端建议间隔轮询；请勿分享会话 ID。</p>
    </section>

    <p v-if="message" class="border-l-2 border-teal-300/70 bg-teal-300/[.06] px-4 py-3 text-sm text-teal-100">{{ message }}</p>
  </div>
</template>
