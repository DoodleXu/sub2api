<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import {
  nativeAvailable,
  nativeCall,
  type APIKeySummary,
  type ConnectionSummary,
  type DeviceSummary,
} from '../native'

type KeyPurpose = 'images' | 'codex' | 'claude'

const purposes: Array<{ value: KeyPurpose; label: string; hint: string }> = [
  { value: 'images', label: 'AI 创作', hint: '生图与编辑任务' },
  { value: 'codex', label: 'Codex', hint: 'Responses API 配置' },
  { value: 'claude', label: 'Claude', hint: 'Claude Code 配置' },
]

const message = ref('')
const busy = ref(false)
const connection = ref<ConnectionSummary | null>(null)
const apiKeys = ref<APIKeySummary[]>([])
const selectedByPurpose = ref<Record<KeyPurpose, number | null>>({ images: null, codex: null, claude: null })
const keyBusy = ref<KeyPurpose | null>(null)
const devices = ref<DeviceSummary[]>([])
const deviceBusy = ref<string | null>(null)

const usableAPIKeys = computed(() => apiKeys.value.filter((key) => isUsableAPIKey(key)))

const currentDeviceID = computed(() => connection.value?.device_id?.trim() || '')

function canRevokeDevice(device: DeviceSummary) {
  // Desktop-scoped tokens are intentionally limited to revoking the current
  // device. Other devices remain manageable from the authenticated web page.
  return Boolean(currentDeviceID.value) && device.device_id === currentDeviceID.value
}

function isUsableAPIKey(key: APIKeySummary) {
  if (key.status.trim().toLowerCase() !== 'active') return false
  if (!key.expires_at) return true
  const expiresAt = Date.parse(key.expires_at)
  return Number.isFinite(expiresAt) && expiresAt > Date.now()
}

async function load() {
  if (!nativeAvailable()) return
  try {
    connection.value = await nativeCall((app) => app.GetConnection())
    if (connection.value.session_configured) {
      await loadAccountResources()
    } else {
      apiKeys.value = []
      devices.value = []
    }
  } catch (error) {
    connection.value = null
    message.value = error instanceof Error ? error.message : '读取账户资源失败'
  }
}

async function loadAccountResources() {
  const [keysResult, devicesResult] = await Promise.allSettled([
    nativeCall((app) => app.ListAPIKeys()),
    nativeCall((app) => app.ListDevices()),
  ])
  const keys = keysResult.status === 'fulfilled' ? keysResult.value : []
  apiKeys.value = keys
  devices.value = devicesResult.status === 'fulfilled' ? devicesResult.value : []
  if (keysResult.status === 'rejected' && devicesResult.status === 'fulfilled') {
    message.value = '当前设备授权未包含 API key 权限；可在客户端配置中重新授权后选择密钥。'
  } else if (keysResult.status === 'rejected' && devicesResult.status === 'rejected') {
    const error = keysResult.reason instanceof Error ? keysResult.reason : devicesResult.reason
    throw (error instanceof Error ? error : new Error('读取账户资源失败'))
  }
  const configuredIDs: Record<KeyPurpose, number | undefined> = {
    images: connection.value?.api_key_id,
    codex: connection.value?.codex_api_key_id,
    claude: connection.value?.claude_api_key_id,
  }
  const usableKeys = keys.filter((key) => isUsableAPIKey(key))
  const fallback = usableKeys[0]?.id ?? null
  for (const purpose of purposes) {
    const current = selectedByPurpose.value[purpose.value]
    const configured = configuredIDs[purpose.value]
    if (configured && usableKeys.some((key) => key.id === configured)) {
      selectedByPurpose.value[purpose.value] = configured
    } else if (current === null || !usableKeys.some((key) => key.id === current)) {
      selectedByPurpose.value[purpose.value] = fallback
    }
  }
}

async function selectAPIKey(purpose: KeyPurpose) {
  if (!nativeAvailable() || keyBusy.value || selectedByPurpose.value[purpose] === null) return
  keyBusy.value = purpose
  try {
    const id = selectedByPurpose.value[purpose]
    const result = await nativeCall((app) => app.SelectAPIKeyForPurpose(purpose, id!))
    connection.value = result.connection
    message.value = `${purpose === 'images' ? 'AI 创作' : purpose === 'codex' ? 'Codex' : 'Claude'} 已使用 ${result.selected.name || result.selected.key_hint}`
  } catch (error) {
    message.value = error instanceof Error ? error.message : '选择 API key 失败'
  } finally {
    keyBusy.value = null
  }
}

async function openKeysPage() {
  if (!nativeAvailable()) return
  try {
    await nativeCall((app) => app.OpenKeysPage())
  } catch (error) {
    message.value = error instanceof Error ? error.message : '打开 API key 管理页失败'
  }
}

async function revokeDevice(device: DeviceSummary) {
  if (!nativeAvailable() || deviceBusy.value) return
  if (!window.confirm(`撤销设备“${device.device_name || device.device_id}”？该设备需要重新授权。`)) return
  deviceBusy.value = device.device_id
  try {
    await nativeCall((app) => app.RevokeDevice(device.device_id))
    message.value = '设备已撤销。'
    await load()
  } catch (error) {
    message.value = error instanceof Error ? error.message : '撤销设备失败'
  } finally {
    deviceBusy.value = null
  }
}

async function checkin() {
  if (!nativeAvailable()) {
    message.value = '请在桌面应用中运行签到。'
    return
  }
  busy.value = true
  try {
    const result = await nativeCall((app) => app.Checkin())
    message.value = result.message || `签到成功，奖励 ${result.reward_amount.toFixed(2)}`
  } catch (error) {
    message.value = error instanceof Error ? error.message : '签到暂不可用'
  } finally {
    busy.value = false
  }
}

async function logout() {
  if (!nativeAvailable() || busy.value) return
  busy.value = true
  try {
    await nativeCall((app) => app.LogoutDevice())
    message.value = '设备会话已注销。'
    await load()
  } catch (error) {
    message.value = error instanceof Error ? error.message : '注销失败'
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="max-w-4xl">
    <p class="max-w-2xl text-sm leading-6 text-muted">账户级操作使用设备授权会话。选择密钥需要 api_keys 权限：完整 API key 只在 Go 层短暂处理并写入系统安全存储，页面只展示名称、状态和掩码。</p>

    <div v-if="connection?.session_configured" class="mt-5 flex flex-wrap items-center gap-3 text-xs text-teal-200">
      <span class="h-2 w-2 rounded-full bg-teal-300" />
      设备会话已连接
      <span v-if="connection.device_id" class="font-mono text-muted">{{ connection.device_id.slice(0, 10) }}…</span>
      <button class="text-muted underline decoration-white/20 underline-offset-4 hover:text-white" :disabled="busy" @click="logout">注销设备</button>
    </div>

    <section class="mt-10 border-t border-white/10 pt-5">
      <div class="flex flex-wrap items-end justify-between gap-4">
        <div>
          <p class="text-xs uppercase tracking-[.18em] text-muted">每日签到</p>
          <h2 class="mt-3 text-xl text-white">领取当日奖励</h2>
          <p class="mt-2 text-sm leading-6 text-muted">资格、奖励和幂等规则由官方服务端判断。</p>
        </div>
        <button class="action-secondary" :disabled="busy || !connection?.session_configured" @click="checkin">{{ busy ? '处理中…' : '签到' }}</button>
      </div>
    </section>

    <section v-if="connection?.session_configured" class="mt-10 border-t border-white/[.08] pt-8">
      <div class="flex flex-wrap items-end justify-between gap-4">
        <div>
          <p class="text-xs uppercase tracking-[.18em] text-muted">API keys</p>
          <h2 class="mt-3 text-lg text-white">按用途选择密钥</h2>
          <p class="mt-2 text-xs leading-5 text-muted">每个用途使用独立的系统 keyring 条目，切换不会覆盖其他客户端配置。</p>
        </div>
        <button class="action-secondary" type="button" @click="openKeysPage">管理 API keys</button>
      </div>
      <div v-if="usableAPIKeys.length" class="mt-5 grid gap-3 md:grid-cols-3">
        <div v-for="purpose in purposes" :key="purpose.value" class="border border-white/[.08] p-4">
          <p class="text-sm text-white">{{ purpose.label }}</p>
          <p class="mt-1 text-xs text-muted">{{ purpose.hint }}</p>
          <select v-model.number="selectedByPurpose[purpose.value]" class="field mt-4" :aria-label="`${purpose.label} API key`">
            <option v-for="key in usableAPIKeys" :key="key.id" :value="key.id">{{ key.name || `Key #${key.id}` }} · {{ key.key_hint }} · active</option>
          </select>
          <button class="action-secondary mt-3 w-full" :disabled="keyBusy !== null" type="button" @click="selectAPIKey(purpose.value)">{{ keyBusy === purpose.value ? '选择中…' : '应用此用途' }}</button>
        </div>
      </div>
      <p v-else class="mt-5 text-xs text-muted">暂无处于 active 且未过期的 API key。</p>
    </section>

    <section v-if="connection?.session_configured" class="mt-10 border-t border-white/[.08] pt-8">
      <p class="text-xs uppercase tracking-[.18em] text-muted">设备</p>
      <h2 class="mt-3 text-lg text-white">已授权客户端</h2>
      <p class="mt-2 text-xs leading-5 text-muted">桌面端只能撤销当前设备；其他设备请在官网的账户安全页面管理。</p>
      <div v-if="devices.length" class="mt-5 space-y-3">
        <div v-for="device in devices" :key="device.device_id" class="flex items-start justify-between gap-3 border-b border-white/[.07] pb-3">
          <div class="min-w-0">
            <p class="truncate text-sm text-white">{{ device.device_name || '未命名设备' }}</p>
            <p class="mt-1 font-mono text-[11px] text-muted">{{ device.device_id.slice(0, 12) }}… · {{ device.protection_level || 'DPoP' }}</p>
          </div>
          <button class="shrink-0 text-xs text-rose-200 hover:text-rose-100 disabled:cursor-not-allowed disabled:opacity-50" :disabled="!!device.revoked_at || !!deviceBusy || !canRevokeDevice(device)" :title="canRevokeDevice(device) ? '撤销当前设备' : '请在官网账户安全页面撤销其他设备'" type="button" @click="revokeDevice(device)">{{ device.revoked_at ? '已撤销' : (deviceBusy === device.device_id ? '撤销中…' : (canRevokeDevice(device) ? '撤销当前设备' : '官网管理')) }}</button>
        </div>
      </div>
      <p v-else class="mt-5 text-xs text-muted">暂无设备记录。</p>
    </section>

    <p v-if="message" class="mt-8 border-l-2 border-teal-300/70 bg-teal-300/[.06] px-4 py-3 text-sm text-teal-100">{{ message }}</p>
  </div>
</template>
