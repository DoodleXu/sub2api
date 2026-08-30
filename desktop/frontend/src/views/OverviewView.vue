<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { nativeAvailable, nativeCall, type ConnectionSummary, type UsageSummary } from '../native'

const connection = ref<ConnectionSummary | null>(null)
const usage = ref<UsageSummary | null>(null)
const loading = ref(false)
const message = ref('')

async function refresh() {
  if (!nativeAvailable()) return
  loading.value = true
  message.value = ''
  try {
    connection.value = await nativeCall((app) => app.GetConnection())
    if (connection.value.api_key_configured || connection.value.session_configured) {
      usage.value = await nativeCall((app) => app.GetUsage())
    } else {
      usage.value = null
    }
  } catch (error) {
    message.value = error instanceof Error ? error.message : '读取连接状态失败'
  } finally {
    loading.value = false
  }
}

onMounted(refresh)
</script>

<template>
  <div class="space-y-8">
    <div class="flex items-end justify-between">
      <div>
        <p class="max-w-xl text-sm leading-6 text-muted">把官方站点连接、账户额度和图像任务放在同一处。首次使用推荐设备授权，API key 仅作为高级兼容模式。</p>
      </div>
      <button class="action-secondary" :disabled="loading || !nativeAvailable()" @click="refresh">{{ loading ? '刷新中…' : '刷新数据' }}</button>
    </div>

    <div v-if="!nativeAvailable()" class="border-l-2 border-amber-300/70 bg-amber-300/[.06] px-5 py-4 text-sm leading-6 text-amber-100">
      当前是前端预览模式。启动 Wails 桌面壳后，连接、余额和生图按钮会调用本机 Go bindings。
    </div>
    <div v-if="message" class="border-l-2 border-rose-300/70 bg-rose-300/[.06] px-5 py-4 text-sm text-rose-100">{{ message }}</div>

    <div class="grid grid-cols-3 gap-4">
      <div class="border-t border-white/10 pt-4">
        <p class="text-xs uppercase tracking-[.18em] text-muted">连接</p>
        <p class="mt-3 text-lg font-medium text-white">{{ connection?.configured ? (connection.label || '官方站点') : '未配置' }}</p>
        <p class="mt-1 truncate text-xs text-muted">{{ connection?.site_url || '—' }}</p>
      </div>
      <div class="border-t border-white/10 pt-4">
        <p class="text-xs uppercase tracking-[.18em] text-muted">可用余额</p>
        <p class="mt-3 text-3xl font-semibold tracking-tight text-teal-300">{{ usage ? `${usage.remaining.toFixed(2)} ${usage.unit || 'USD'}` : '—' }}</p>
        <p class="mt-1 text-xs text-muted">{{ usage?.plan_name || '配置后自动读取' }}</p>
      </div>
      <div class="border-t border-white/10 pt-4">
        <p class="text-xs uppercase tracking-[.18em] text-muted">API key</p>
        <p class="mt-3 text-lg font-medium text-white">{{ connection?.session_configured ? '设备会话已连接' : (connection?.api_key_configured ? '已安全保存' : '等待授权') }}</p>
        <p class="mt-1 font-mono text-xs text-muted">{{ connection?.session_configured ? (connection.device_id ? `设备 ${connection.device_id.slice(0, 10)}…` : 'token 在系统安全存储') : (connection?.api_key_hint || '不会显示完整密钥') }}</p>
      </div>
    </div>

    <div class="grid grid-cols-[1.4fr_1fr] gap-8 border-t border-white/[.07] pt-8">
      <div>
        <p class="text-xs uppercase tracking-[.18em] text-muted">下一步</p>
        <div class="mt-4 space-y-3 text-sm">
          <RouterLink to="/connect" class="group flex items-center justify-between border-b border-white/[.07] py-3 text-muted transition hover:text-white">
            <span><span class="mr-3 font-mono text-xs text-teal-300">01</span>官方站点一键授权</span><span class="transition group-hover:translate-x-1">→</span>
          </RouterLink>
          <RouterLink to="/studio" class="group flex items-center justify-between border-b border-white/[.07] py-3 text-muted transition hover:text-white">
            <span><span class="mr-3 font-mono text-xs text-teal-300">02</span>打开 AI 创作</span><span class="transition group-hover:translate-x-1">→</span>
          </RouterLink>
          <RouterLink to="/recharge" class="group flex items-center justify-between border-b border-white/[.07] py-3 text-muted transition hover:text-white">
            <span><span class="mr-3 font-mono text-xs text-teal-300">03</span>打开充值入口</span><span class="transition group-hover:translate-x-1">→</span>
          </RouterLink>
          <RouterLink to="/account" class="group flex items-center justify-between border-b border-white/[.07] py-3 text-muted transition hover:text-white">
            <span><span class="mr-3 font-mono text-xs text-teal-300">04</span>管理账户与设备</span><span class="transition group-hover:translate-x-1">→</span>
          </RouterLink>
        </div>
      </div>
      <div class="rounded-md border border-teal-300/20 bg-teal-300/[.05] p-6">
        <p class="text-xs uppercase tracking-[.18em] text-teal-300">本地优先</p>
        <p class="mt-4 max-w-xs text-xl leading-8 text-white">任务结果直接落盘，签名 URL 过期也不影响本地图库。</p>
      </div>
    </div>
  </div>
</template>
