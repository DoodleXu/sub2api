<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { nativeAvailable, nativeCall, type AppInfo, type ConnectionSummary, type ToolConfigResult } from '../native'

const props = defineProps<{ embedded?: boolean }>()

const info = ref<AppInfo | null>(null)
const connection = ref<ConnectionSummary | null>(null)
const message = ref('')
const clearing = ref(false)
const integrating = ref<string | null>(null)
const integration = ref<ToolConfigResult | null>(null)
const copied = ref(false)

function toolKeyConfigured(tool: 'codex' | 'claude') {
  const current = connection.value
  if (!current) return false
  const purposeSelected = tool === 'codex' ? current.codex_api_key_id : current.claude_api_key_id
  const deviceConnection = current.auth_mode === 'device' || current.session_configured
  return deviceConnection ? Number(purposeSelected || 0) > 0 : current.api_key_configured
}

async function load() {
  if (!nativeAvailable()) return
  try {
    info.value = await nativeCall((app) => app.GetAppInfo())
    connection.value = await nativeCall((app) => app.GetConnection())
  } catch (error) {
    message.value = error instanceof Error ? error.message : '读取应用信息失败'
  }
}

async function clearConnection() {
  if (!nativeAvailable() || clearing.value) return
  if (!window.confirm('清除本地连接配置？图片库和任务记录不会删除。')) return
  clearing.value = true
  try {
    await nativeCall((app) => app.ClearConnection())
    connection.value = null
    message.value = '连接配置已清除。'
  } catch (error) {
    message.value = error instanceof Error ? error.message : '清除失败'
  } finally {
    clearing.value = false
  }
}

async function integrateTool(tool: 'codex' | 'claude') {
  if (!nativeAvailable() || integrating.value) return
  if (!toolKeyConfigured(tool)) {
    message.value = tool === 'codex'
      ? '请先为 Codex 选择可用 API key，再写入本地客户端配置。'
      : '请先为 Claude Code 选择可用 API key，再写入本地客户端配置。'
    return
  }
  const warning = tool === 'codex'
    ? 'Codex 只会更新 provider 配置，API key 留在系统安全存储；启动时请使用下方安全启动命令。是否继续？'
    : 'Claude Code 会把 API key 写入本机 settings.json；同机可读取该文件的用户或进程可能复制密钥。是否继续？'
  if (!window.confirm(warning)) return
  integrating.value = tool
  message.value = ''
  integration.value = null
  try {
    integration.value = await nativeCall((app) => app.IntegrateToolConfig({ tool }))
    message.value = `${tool === 'codex' ? 'Codex' : 'Claude Code'} 配置已合并；原文件已自动备份。`
  } catch (error) {
    message.value = error instanceof Error ? error.message : '客户端配置失败'
  } finally {
    integrating.value = null
  }
}

async function restoreFile(filePath: string, tool: 'codex' | 'claude') {
  if (!nativeAvailable() || !filePath) return
  try {
    const result = await nativeCall((app) => app.RestoreToolConfig({ tool, backup_path: filePath }))
    message.value = result.previous_backup_path
      ? `已恢复；恢复前版本备份在 ${result.previous_backup_path}`
      : '已恢复配置。'
  } catch (error) {
    message.value = error instanceof Error ? error.message : '恢复配置失败'
  }
}

async function copyLaunchCommand() {
  const command = integration.value?.launch?.command
  if (!command) return
  try {
    await navigator.clipboard.writeText(command)
    copied.value = true
    window.setTimeout(() => { copied.value = false }, 2500)
  } catch {
    message.value = '当前环境不允许访问剪贴板，请手动选择命令。'
  }
}

onMounted(load)
</script>

<template>
  <div class="max-w-3xl space-y-10">
    <section v-if="!props.embedded"><p class="text-xs uppercase tracking-[.18em] text-muted">关于</p><div class="mt-4 flex items-end justify-between border-b border-white/[.08] pb-5"><div><h2 class="text-xl text-white">神奇AI助手</h2><p class="mt-1 text-sm text-muted">Wails v2 跨平台客户端</p></div><span class="font-mono text-xs text-muted">{{ info?.version || '0.1.0' }}</span></div></section>
    <section v-if="!props.embedded"><p class="text-xs uppercase tracking-[.18em] text-muted">本地连接</p><p class="mt-4 text-sm text-white">{{ connection?.site_url || info?.official_site_url || '尚未配置连接' }}</p><p class="mt-1 text-xs text-muted">仅允许官方站点；连接元数据不含密钥。Claude Code 的 settings.json 会在确认后保存认证字段。</p><button class="action-secondary mt-5 border-rose-300/20 text-rose-200 hover:border-rose-300/50" :disabled="clearing || !connection?.configured" @click="clearConnection">{{ clearing ? '清除中…' : '清除连接配置' }}</button></section>
    <section class="border-t border-white/[.08] pt-5"><p class="text-xs uppercase tracking-[.18em] text-muted">开发工具一键配置</p><p class="mt-3 max-w-2xl text-sm leading-6 text-muted">把已保存的 API key 合并到本机 Codex 或 Claude Code 配置，保留其他字段；原文件会生成 0600 备份。设备授权会话不能直接转换成 CLI API key。</p><div class="mt-5 flex flex-wrap gap-3"><button class="action-secondary" :disabled="!!integrating || !toolKeyConfigured('codex')" @click="integrateTool('codex')">{{ integrating === 'codex' ? '写入 Codex…' : '配置 Codex' }}</button><button class="action-secondary" :disabled="!!integrating || !toolKeyConfigured('claude')" @click="integrateTool('claude')">{{ integrating === 'claude' ? '写入 Claude…' : '配置 Claude Code' }}</button></div><div v-if="integration" class="mt-5 space-y-2 text-xs text-muted"><div v-for="file in integration.files" :key="file.path" class="flex flex-col gap-1 border-l border-white/10 pl-3"><span class="break-all text-white">{{ file.path }} <span v-if="file.changed" class="text-teal-300">· 已更新</span><span v-else>· 无变化</span></span><span v-if="file.backup_path" class="break-all">备份：{{ file.backup_path }} <button class="ml-2 text-teal-300 underline" @click="restoreFile(file.backup_path, integration!.tool as 'codex' | 'claude')">恢复</button></span></div><div v-if="integration.launch" class="border-l border-teal-300/40 pl-3"><p class="text-teal-200">安全启动命令（{{ integration.launch.shell }}）</p><code class="mt-2 block select-all whitespace-pre-wrap break-words text-[11px] text-white">{{ integration.launch.command }}</code><button class="mt-2 text-teal-300 underline" @click="copyLaunchCommand">{{ copied ? '已复制' : '复制启动命令' }}</button><p v-if="integration.launch.note" class="mt-1 leading-5 text-muted">{{ integration.launch.note }}</p></div><p v-for="warning in integration.warnings" :key="warning" class="border-l border-amber-300/60 pl-3 text-amber-200">{{ warning }}</p></div></section>
    <section v-if="!props.embedded" class="border-t border-white/[.08] pt-5"><p class="text-xs uppercase tracking-[.18em] text-muted">数据目录</p><p class="mt-3 text-sm leading-6 text-muted">配置、任务 checkpoint 和图片文件由 Go 层写入系统应用数据目录，并使用 0600/0700 权限与原子替换。</p></section>
    <p v-if="message" class="text-sm text-teal-300">{{ message }}</p>
  </div>
</template>
