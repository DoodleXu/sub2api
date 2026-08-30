<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { nativeAvailable, nativeCall, type ConnectionSummary, type DeviceAuthorizationStatus, type DeviceAuthorizationView } from '../native'
import SettingsView from './SettingsView.vue'

const form = reactive({ site_url: 'https://ai.clol.site', gateway_url: '', api_key: '', label: '' })
const current = ref<ConnectionSummary | null>(null)
const saving = ref(false)
const probing = ref(false)
const message = ref('')
const error = ref('')
const deviceAuth = ref<DeviceAuthorizationView | null>(null)
const deviceStatus = ref<DeviceAuthorizationStatus | null>(null)
const deviceStarting = ref(false)
const deviceOpening = ref(false)
const deviceScopeOptions = [
  { value: 'checkin', label: '每日签到', hint: '允许客户端执行签到' },
  { value: 'images', label: '生图历史', hint: '允许读取/删除生图任务与结果' },
  { value: 'billing', label: '托管充值', hint: '允许创建并查询短期 checkout 会话（高风险）' },
  { value: 'api_keys', label: '读取现有 API key', hint: '允许读取完整密钥并保存到本机系统凭证库（高风险）' },
]
const selectedDeviceScopes = reactive<Record<string, boolean>>({
  checkin: false,
  images: false,
  billing: false,
  api_keys: false,
})
const requestedDeviceScopes = computed(() => [
  'openid',
  'profile',
  'balance',
  'usage',
  ...deviceScopeOptions.filter((option) => selectedDeviceScopes[option.value]).map((option) => option.value),
])
let deviceTimer: number | undefined

async function load() {
  if (!nativeAvailable()) return
  try {
    current.value = await nativeCall((app) => app.GetConnection())
    // Site URL is pinned in the native layer; never let an old/foreign
    // metadata file repopulate an editable origin field.
    form.site_url = 'https://ai.clol.site'
    if (current.value.gateway_url && current.value.gateway_url !== current.value.site_url) form.gateway_url = current.value.gateway_url
    form.label = current.value.label
  } catch (loadError) {
    error.value = loadError instanceof Error ? loadError.message : '读取配置失败'
  }
}

async function save() {
  error.value = ''
  message.value = ''
  if (!form.site_url.trim() || !form.api_key.trim()) {
    error.value = '站点地址和 API key 均为必填项。'
    return
  }
  if (!nativeAvailable()) {
    error.value = '请在桌面应用中保存连接。'
    return
  }
  saving.value = true
  try {
    current.value = await nativeCall((app) => app.SaveConnection({ ...form }))
    form.api_key = ''
    message.value = '连接已保存。完整密钥不会写入普通配置文件。'
  } catch (saveError) {
    error.value = saveError instanceof Error ? saveError.message : '保存连接失败'
  } finally {
    saving.value = false
  }
}

function stopDevicePolling() {
  if (deviceTimer !== undefined) window.clearTimeout(deviceTimer)
  deviceTimer = undefined
}

async function beginDeviceLogin() {
  if (!nativeAvailable() || deviceStarting.value) return
  stopDevicePolling()
  error.value = ''
  message.value = ''
  deviceStarting.value = true
  deviceAuth.value = null
  deviceStatus.value = null
  try {
    deviceAuth.value = await nativeCall((app) => app.BeginDeviceAuthorization({
      device_name: form.label || '神奇AI助手',
      scopes: requestedDeviceScopes.value,
    }))
    message.value = '授权码已生成，请在官方站点完成确认。'
    await openDevicePage()
    scheduleDevicePoll()
  } catch (deviceError) {
    error.value = deviceError instanceof Error ? deviceError.message : '启动设备授权失败'
  } finally {
    deviceStarting.value = false
  }
}

async function openDevicePage() {
  if (!deviceAuth.value || !nativeAvailable() || deviceOpening.value) return
  deviceOpening.value = true
  try {
    await nativeCall((app) => app.OpenDeviceVerification(deviceAuth.value!.request_id))
  } catch (openError) {
    error.value = openError instanceof Error ? openError.message : '打开官方授权页失败'
  } finally {
    deviceOpening.value = false
  }
}

function scheduleDevicePoll() {
  stopDevicePolling()
  const delay = Math.max(2, deviceAuth.value?.interval || 5) * 1000
  deviceTimer = window.setTimeout(pollDeviceLogin, delay)
}

async function pollDeviceLogin() {
  if (!deviceAuth.value || !nativeAvailable()) return
  try {
    const result = await nativeCall((app) => app.PollDeviceAuthorization(deviceAuth.value!.request_id))
    deviceStatus.value = result
    if (result.status === 'pending') {
      scheduleDevicePoll()
    } else if (result.status === 'authorized') {
      stopDevicePolling()
      message.value = result.message || '设备已授权，连接已就绪。'
      deviceAuth.value = null
      await load()
    } else {
      stopDevicePolling()
      error.value = result.message || '设备授权未完成'
    }
  } catch (pollError) {
    error.value = pollError instanceof Error ? pollError.message : '授权状态查询失败'
    scheduleDevicePoll()
  }
}

async function probe() {
  if (!nativeAvailable()) return
  probing.value = true
  error.value = ''
  try {
    const result = await nativeCall((app) => app.ProbeConnection())
    message.value = result.site_name ? `已连接：${result.site_name}` : '站点连接正常。'
    if (result.gateway_url && result.gateway_url !== form.gateway_url) form.gateway_url = result.gateway_url
  } catch (probeError) {
    error.value = probeError instanceof Error ? probeError.message : '连接检查失败'
  } finally {
    probing.value = false
  }
}

onMounted(load)
onUnmounted(stopDevicePolling)
</script>

<template>
  <div class="grid grid-cols-[1.1fr_.9fr] gap-12">
    <div>
      <p class="max-w-lg text-sm leading-6 text-muted">首选设备授权：客户端固定连接官方站点，在浏览器确认后自动安全保存会话。下方 API key 兼容模式同样只接受官方站点。</p>
      <section class="mt-8 border-b border-white/[.08] pb-8">
        <div class="flex items-start justify-between gap-4"><div><p class="text-xs uppercase tracking-[.18em] text-teal-300/80">推荐方式</p><h2 class="mt-2 text-xl text-white">官方站点一键授权</h2><p class="mt-2 text-sm leading-6 text-muted">不会询问账号密码。设备私钥交给系统安全存储，短期 access token 只留在当前进程。</p></div><span class="rounded-full border border-teal-300/20 px-3 py-1 text-xs text-teal-200">ai.clol.site</span></div>
        <div class="mt-5 grid gap-2 sm:grid-cols-2">
          <label v-for="option in deviceScopeOptions" :key="option.value" class="flex cursor-pointer items-start gap-3 border border-white/[.08] p-3 text-sm text-ink">
            <input v-model="selectedDeviceScopes[option.value]" class="mt-1" type="checkbox" :disabled="deviceStarting" />
            <span><span class="block text-white">{{ option.label }}</span><span class="mt-1 block text-xs leading-5 text-muted">{{ option.hint }}</span></span>
          </label>
        </div>
        <p class="mt-3 text-xs leading-5 text-muted">默认只申请账户、余额和用量读取权限。高风险权限会在浏览器确认页再次展示，你可以在那里取消。</p>
        <button class="action-primary mt-5" :disabled="deviceStarting || !nativeAvailable()" type="button" @click="beginDeviceLogin">{{ deviceStarting ? '准备授权…' : '开始设备授权' }}</button>
        <div v-if="deviceAuth" class="mt-5 border border-teal-300/20 bg-teal-300/[.05] p-4"><div class="flex items-center justify-between"><div><p class="text-xs text-muted">请在浏览器确认这个代码</p><p class="mt-1 font-mono text-2xl tracking-[.16em] text-teal-200">{{ deviceAuth.user_code }}</p></div><button class="action-secondary" :disabled="deviceOpening" type="button" @click="openDevicePage">{{ deviceOpening ? '打开中…' : '重新打开授权页' }}</button></div><p class="mt-3 break-all text-xs text-muted">{{ deviceAuth.verification_url }}</p><p class="mt-2 text-xs" :class="deviceStatus?.status === 'error' ? 'text-rose-200' : 'text-muted'">{{ deviceStatus?.message || '等待浏览器确认…' }}</p></div>
      </section>
      <form class="mt-8 space-y-5" @submit.prevent="save">
        <div><p class="text-xs uppercase tracking-[.18em] text-muted">高级兼容模式</p></div>
        <label class="block text-sm text-ink">官方站点地址<span class="ml-1 text-teal-300">*</span><input v-model="form.site_url" class="field" type="url" readonly autocomplete="url" /></label>
        <label class="block text-sm text-ink">Gateway 地址<span class="ml-2 text-xs text-muted">由官方能力接口自动发现</span><input v-model="form.gateway_url" class="field" type="url" readonly placeholder="保存后自动发现" autocomplete="url" /></label>
        <label class="block text-sm text-ink">API key<span class="ml-1 text-teal-300">*</span><input v-model="form.api_key" class="field" type="password" placeholder="sk-…" autocomplete="off" /></label>
        <label class="block text-sm text-ink">连接名称<input v-model="form.label" class="field" type="text" placeholder="我的工作站" maxlength="80" /></label>
        <div class="flex items-center gap-3 pt-2"><button class="action-primary" :disabled="saving" type="submit">{{ saving ? '保存中…' : '保存连接' }}</button><button class="action-secondary" :disabled="probing || !current?.configured" type="button" @click="probe">{{ probing ? '检查中…' : '测试站点' }}</button></div>
      </form>
      <p v-if="message" class="mt-5 text-sm text-teal-300">{{ message }}</p>
      <p v-if="error" class="mt-5 text-sm text-rose-200">{{ error }}</p>
    </div>

    <div class="border-l border-white/[.08] pl-10">
      <p class="text-xs uppercase tracking-[.18em] text-muted">当前连接</p>
      <div class="mt-5 space-y-5">
        <div><p class="text-xs text-muted">站点</p><p class="mt-1 break-all text-sm text-white">{{ current?.site_url || '尚未配置' }}</p></div>
        <div><p class="text-xs text-muted">Gateway</p><p class="mt-1 break-all text-sm text-white">{{ current?.gateway_url || '保存后自动发现' }}</p></div>
        <div><p class="text-xs text-muted">密钥状态</p><p class="mt-1 text-sm text-teal-300">{{ current?.api_key_configured ? `已保存 · ${current.api_key_hint}` : '未保存' }}</p></div>
        <div v-if="current?.session_configured"><p class="text-xs text-muted">设备保护级别</p><p class="mt-1 text-sm text-white">{{ current.protection_level || 'DPoP 设备绑定' }}</p></div>
      </div>
      <div class="mt-12 border-t border-white/[.08] pt-5 text-xs leading-6 text-muted">密钥、refresh token 与设备私钥交给 macOS Keychain 或 Windows Credential Manager；短期 access token 只驻留内存。Codex 配置只保留固定引用，Claude Code 会在确认后写入 settings.json。</div>
    </div>
  </div>
  <section class="mt-16 border-t border-white/[.08] pt-10">
    <SettingsView embedded :key="`${current?.updated_at || ''}:${current?.auth_mode || ''}:${current?.codex_api_key_id || 0}:${current?.claude_api_key_id || 0}`" />
  </section>
</template>
