<template>
  <div class="min-h-screen bg-gray-50 px-4 py-12 dark:bg-dark-950">
    <div class="mx-auto max-w-lg rounded-2xl border border-gray-200 bg-white p-8 shadow-sm dark:border-dark-700 dark:bg-dark-900">
      <div class="mb-8">
        <p class="text-xs font-semibold uppercase tracking-[.2em] text-primary-600 dark:text-primary-400">Sub2API Desktop</p>
        <h1 class="mt-3 text-2xl font-bold text-gray-900 dark:text-white">确认绑定桌面设备</h1>
        <p class="mt-3 text-sm leading-6 text-gray-600 dark:text-dark-300">
          确认后，神奇AI助手只能在生成授权的那台设备上使用这个会话。浏览器不会把密码、API key 或 token 返回给桌面端。
        </p>
      </div>

      <div v-if="!authenticated" class="rounded-xl bg-amber-50 px-4 py-3 text-sm leading-6 text-amber-800 dark:bg-amber-400/10 dark:text-amber-200">
        请先登录你的官方站点账号，登录完成后会自动回到此确认页。
        <button class="ml-1 font-semibold underline underline-offset-4" type="button" @click="goToLogin">前往登录</button>
      </div>

      <form v-else class="space-y-5" @submit.prevent="approve(true)">
        <div>
          <label class="input-label" for="desktop-device-code">设备确认码</label>
          <input
            id="desktop-device-code"
            v-model="userCode"
            class="input mt-2 w-full font-mono uppercase tracking-[.18em]"
            autocomplete="one-time-code"
            inputmode="text"
            maxlength="16"
            placeholder="例如 ABCDE-FGHIJ"
            :disabled="busy || finished"
            @input="clearDetails"
            @blur="loadDetails"
            required
          />
          <p class="mt-2 text-xs text-gray-500 dark:text-dark-400">确认码只用于这一次五分钟授权请求，过期后请从桌面端重新生成。</p>
          <button v-if="!details" class="btn btn-secondary mt-3" type="button" :disabled="busy || loadingDetails || !normalizedCode" @click="loadDetails">{{ loadingDetails ? '读取中…' : '查看授权详情' }}</button>
        </div>
        <div v-if="loadingDetails" class="rounded-xl bg-gray-50 px-4 py-3 text-sm text-gray-600 dark:bg-dark-800 dark:text-dark-300">正在读取授权详情…</div>
        <div v-if="details" class="space-y-4 rounded-xl border border-gray-200 bg-gray-50 p-4 dark:border-dark-700 dark:bg-dark-800">
          <div class="grid gap-3 text-sm sm:grid-cols-2">
            <div><p class="text-xs text-gray-500 dark:text-dark-400">设备</p><p class="mt-1 font-medium text-gray-900 dark:text-white">{{ details.device_name }}</p></div>
            <div><p class="text-xs text-gray-500 dark:text-dark-400">客户端</p><p class="mt-1 font-mono text-gray-900 dark:text-white">{{ details.client_id }}</p></div>
            <div><p class="text-xs text-gray-500 dark:text-dark-400">受众</p><p class="mt-1 font-mono text-gray-900 dark:text-white">{{ details.audience }}</p></div>
            <div><p class="text-xs text-gray-500 dark:text-dark-400">设备保护</p><p class="mt-1 text-gray-900 dark:text-white">{{ details.protection_level || '标准' }}</p></div>
          </div>
          <div>
            <p class="text-xs text-gray-500 dark:text-dark-400">请求的权限（可取消勾选）</p>
            <div class="mt-2 space-y-2">
              <label v-for="scope in details.scopes" :key="scope" class="flex cursor-pointer items-start gap-3 rounded-lg border border-gray-200 bg-white p-3 text-sm dark:border-dark-700 dark:bg-dark-900">
                <input class="mt-1" type="checkbox" :checked="selectedScopeSet.has(scope)" :disabled="busy" @change="toggleScope(scope)" />
                <span class="min-w-0"><span class="block text-gray-900 dark:text-white">{{ scopeLabel(scope) }}<span v-if="highRiskScopes.has(scope)" class="ml-2 rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-semibold text-amber-800 dark:bg-amber-400/10 dark:text-amber-200">高风险</span></span><span class="mt-1 block text-xs leading-5 text-gray-500 dark:text-dark-400">{{ highRiskScopes.has(scope) || !defaultApprovalScopes.has(scope) ? '仅在你明确勾选后授予。' : '只用于本客户端对应功能。' }}</span></span>
              </label>
            </div>
            <p class="mt-2 text-xs leading-5 text-gray-500 dark:text-dark-400">本次授权将在约 {{ details.expires_in }} 秒后失效；未勾选的权限不会写入设备令牌。</p>
          </div>
        </div>
        <div v-if="errorMessage" class="rounded-xl bg-red-50 px-4 py-3 text-sm leading-6 text-red-700 dark:bg-red-400/10 dark:text-red-200" role="alert">{{ errorMessage }}</div>
        <div v-if="finished" class="rounded-xl bg-emerald-50 px-4 py-3 text-sm leading-6 text-emerald-700 dark:bg-emerald-400/10 dark:text-emerald-200" role="status">{{ successMessage }}</div>
        <div v-if="!finished" class="flex flex-wrap gap-3">
          <button class="btn btn-primary" type="submit" :disabled="busy || !canApprove">{{ busy ? '确认中…' : '确认绑定' }}</button>
          <button class="btn btn-secondary" type="button" :disabled="busy || !normalizedCode" @click="approve(false)">拒绝</button>
        </div>
      </form>

      <p v-if="finished" class="mt-6 text-sm text-gray-600 dark:text-dark-300">现在可以回到桌面应用，等待授权完成。</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { apiClient } from '@/api/client'

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()
const userCode = ref(typeof route.query.user_code === 'string' ? route.query.user_code : '')
const busy = ref(false)
const finished = ref(false)
const errorMessage = ref('')
const successMessage = ref('设备已确认绑定。')
const details = ref<{
  client_id: string
  device_name: string
  scopes: string[]
  audience: string
  protection_level: string
  expires_in: number
} | null>(null)
const selectedScopes = ref<string[]>([])
const loadingDetails = ref(false)
const detailsCode = ref('')

const authenticated = computed(() => authStore.isAuthenticated)
const normalizedCode = computed(() => userCode.value.trim().toUpperCase().replace(/\s+/g, ''))
const scopeLabels: Record<string, string> = {
  openid: '账户身份（openid）',
  profile: '账户资料与余额（profile）',
  balance: '余额读取（balance）',
  usage: '用量与统计读取（usage）',
  checkin: '执行每日签到（checkin）',
  images: '生图任务与历史（images）',
  billing: '创建并查询托管充值会话（billing）',
  api_keys: '读取并保存现有 API key（api_keys）',
}
const highRiskScopes = new Set(['billing', 'api_keys'])
const defaultApprovalScopes = new Set(['openid', 'profile', 'balance', 'usage'])
const selectedScopeSet = computed(() => new Set(selectedScopes.value))
const canApprove = computed(() => Boolean(normalizedCode.value && details.value && detailsCode.value === normalizedCode.value && selectedScopes.value.length > 0))

function goToLogin(): void {
  void router.push({ path: '/login', query: { redirect: route.fullPath } })
}

function scopeLabel(scope: string): string {
  return scopeLabels[scope] || scope
}

function toggleScope(scope: string): void {
  const next = new Set(selectedScopes.value)
  if (next.has(scope)) next.delete(scope)
  else next.add(scope)
  selectedScopes.value = Array.from(next)
}

function clearDetails(): void {
  details.value = null
  detailsCode.value = ''
  selectedScopes.value = []
  errorMessage.value = ''
}

async function loadDetails(): Promise<void> {
  if (!authenticated.value || !normalizedCode.value || loadingDetails.value) return
  const requestedCode = normalizedCode.value
  loadingDetails.value = true
  errorMessage.value = ''
  try {
    const response = await apiClient.get<typeof details.value>('/user/device/approval', {
      params: { user_code: requestedCode },
    })
    // The user may have typed a different code while the request was in
    // flight. Never render (or approve with) a response for an older code.
    if (requestedCode !== normalizedCode.value) return
    if (!response.data) throw new Error('授权请求不存在或已过期。')
    details.value = response.data
    detailsCode.value = requestedCode
    // Read-only account scopes are selected for convenience. Billing and API
    // key access always require an explicit click on this browser page.
    selectedScopes.value = response.data.scopes.filter((scope) => defaultApprovalScopes.has(scope))
  } catch (error) {
    if (requestedCode !== normalizedCode.value) return
    const message = (error as { message?: string })?.message
    errorMessage.value = message || '读取授权详情失败，请检查确认码是否过期。'
  } finally {
    loadingDetails.value = false
    // If input changed during the request, load the latest code after the
    // in-flight request has released the guard above.
    if (requestedCode !== normalizedCode.value && normalizedCode.value) void loadDetails()
  }
}

async function approve(approved: boolean): Promise<void> {
  if (!normalizedCode.value || busy.value || finished.value) return
  busy.value = true
  errorMessage.value = ''
  try {
    if (approved && !canApprove.value) {
      errorMessage.value = '请至少选择一项权限。'
      busy.value = false
      return
    }
    await apiClient.post('/user/device/approve', {
      user_code: normalizedCode.value,
      approved,
      scopes: approved ? selectedScopes.value : [],
    })
    finished.value = true
    successMessage.value = approved ? '设备已确认绑定。' : '已拒绝这次设备绑定请求。'
  } catch (error) {
    const message = (error as { message?: string })?.message
    errorMessage.value = message || '确认失败，请检查确认码是否过期或已经使用。'
  } finally {
    busy.value = false
  }
}

onMounted(() => {
  if (!authenticated.value) goToLogin()
  else {
    // Keep the one-time code in component memory while removing it from the
    // address bar/history once an authenticated browser has captured it. This
    // reduces accidental leakage through screenshots, browser sync and referrer
    // logs without breaking the unauthenticated login redirect above.
    if (typeof route.query.user_code === 'string') {
      void router.replace({ query: { ...route.query, user_code: undefined } })
    }
    void loadDetails()
  }
})
</script>
