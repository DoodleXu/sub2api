<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { nativeAvailable, nativeCall, type UsageOverview, type UsageSnapshot } from '../native'

const overview = ref<UsageOverview | null>(null)
const loading = ref(false)
const message = ref('')

function snapshotAvailable(snapshot: UsageSnapshot | null | undefined, ready?: boolean) {
  if (!overview.value || overview.value.available === false || !snapshot) return false
  if (snapshot.available === false || snapshot.valid === false || ready === false) return false
  return Number.isFinite(Number(snapshot.remaining)) || Number.isFinite(Number(snapshot.balance))
}

function amount(snapshot: UsageSnapshot | null | undefined, ready?: boolean) {
  if (!snapshotAvailable(snapshot, ready)) return '暂无数据'
  const value = Number.isFinite(Number(snapshot?.remaining)) ? Number(snapshot?.remaining) : Number(snapshot?.balance)
  return `${value.toFixed(2)} ${snapshot?.unit || ''}`.trim()
}

function detail(snapshot: UsageSnapshot | null | undefined, ready?: boolean) {
  if (!snapshotAvailable(snapshot, ready)) return '暂无数据'
  const bits = [snapshot?.plan_name, snapshot?.status].filter(Boolean)
  return bits.length ? bits.join(' · ') : '已读取'
}

function stats(snapshot: UsageSnapshot | null | undefined, ready?: boolean) {
  if (!snapshotAvailable(snapshot, ready) || snapshot?.stats_available !== true) return '统计暂无数据'
  const requests = Number(snapshot.total_requests)
  const tokens = Number(snapshot.total_tokens)
  const cost = Number(snapshot.total_actual_cost ?? snapshot.total_cost)
  const values: string[] = []
  if (Number.isFinite(requests)) values.push(`${requests.toLocaleString()} 次请求`)
  if (Number.isFinite(tokens)) values.push(`${tokens.toLocaleString()} tokens`)
  if (Number.isFinite(cost)) values.push(`累计扣除 ${cost.toFixed(4)} USD`)
  return values.length ? values.join(' · ') : '统计暂无数据'
}

async function refresh() {
  if (!nativeAvailable() || loading.value) return
  loading.value = true
  message.value = ''
  try {
    overview.value = await nativeCall((app) => app.GetUsageOverview())
  } catch (error) {
    overview.value = null
    message.value = error instanceof Error ? error.message : '读取用量失败'
  } finally {
    loading.value = false
  }
}

onMounted(refresh)
</script>

<template>
  <div class="max-w-4xl space-y-8">
    <div class="flex flex-wrap items-end justify-between gap-4">
      <div>
        <p class="max-w-2xl text-sm leading-6 text-muted">账户总用量来自设备会话；所选 key 用量来自当前 images 用途密钥。两个数据源相互独立，缺少权限时不会用 0 伪装。</p>
      </div>
      <button class="action-secondary" :disabled="loading || !nativeAvailable()" type="button" @click="refresh">{{ loading ? '读取中…' : '刷新用量' }}</button>
    </div>

    <div v-if="!nativeAvailable()" class="border-l-2 border-amber-300/70 bg-amber-300/[.06] px-5 py-4 text-sm leading-6 text-amber-100">当前是前端预览模式，启动神奇AI助手桌面壳后才能读取用量。</div>
    <div v-if="message" class="border-l-2 border-rose-300/70 bg-rose-300/[.06] px-5 py-4 text-sm text-rose-100">{{ message }}</div>

    <div class="grid gap-8 md:grid-cols-2">
      <section class="border-t border-teal-300/50 pt-5">
        <p class="text-xs uppercase tracking-[.18em] text-muted">账户总用量</p>
        <p class="mt-4 text-3xl font-semibold tracking-tight text-teal-300">{{ amount(overview?.account, overview?.account_ready) }}</p>
        <p class="mt-2 text-sm text-muted">{{ detail(overview?.account, overview?.account_ready) }}</p>
        <p class="mt-3 text-xs text-muted">{{ stats(overview?.account, overview?.account_ready) }}</p>
        <p class="mt-5 text-xs leading-5 text-muted">设备会话余额与账户计划状态。没有设备授权时显示“暂无数据”。</p>
      </section>
      <section class="border-t border-teal-300/50 pt-5">
        <p class="text-xs uppercase tracking-[.18em] text-muted">所选 API key</p>
        <p class="mt-4 text-3xl font-semibold tracking-tight text-teal-300">{{ amount(overview?.selected_key, overview?.selected_key_ready) }}</p>
        <p class="mt-2 text-sm text-muted">{{ detail(overview?.selected_key, overview?.selected_key_ready) }}</p>
        <p class="mt-3 text-xs text-muted">{{ stats(overview?.selected_key, overview?.selected_key_ready) }}</p>
        <p class="mt-5 text-xs leading-5 text-muted">当前 images 用途密钥的网关额度。密钥撤销或尚未选择时显示“暂无数据”。</p>
      </section>
    </div>

    <p v-if="overview?.as_of" class="text-xs text-muted">数据时间：{{ overview.as_of }}</p>
  </div>
</template>
