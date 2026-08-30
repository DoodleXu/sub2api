<script setup lang="ts">
import { computed, ref } from 'vue'
import { RouterLink, RouterView, useRoute } from 'vue-router'
import { nativeAvailable, nativeCall } from './native'

const route = useRoute()
const probing = ref(false)
const probeMessage = ref('')

const navigation = [
  { to: '/overview', label: '概览', mark: '01' },
  { to: '/connect', label: '客户端配置', mark: '02' },
  { to: '/studio', label: 'AI 创作', mark: '03' },
  { to: '/usage', label: '用量', mark: '04' },
  { to: '/recharge', label: '充值', mark: '05' },
  { to: '/account', label: '账户与设备', mark: '06' },
]

const currentTitle = computed(() => String(route.meta.title || '概览'))
const currentEyebrow = computed(() => String(route.meta.eyebrow || '神奇AI助手'))

async function probe() {
  if (!nativeAvailable() || probing.value) return
  probing.value = true
  probeMessage.value = ''
  try {
    const result = await nativeCall((app) => app.ProbeConnection())
    probeMessage.value = result.reachable ? '站点连接正常' : (result.message || '暂时无法连接')
  } catch (error) {
    probeMessage.value = error instanceof Error ? error.message : '连接检查失败'
  } finally {
    probing.value = false
    window.setTimeout(() => { probeMessage.value = '' }, 3500)
  }
}
</script>

<template>
  <div class="min-h-screen bg-transparent text-ink">
    <aside class="fixed inset-y-0 left-0 flex w-[248px] flex-col border-r border-white/[.07] bg-surface/80 px-5 py-6 backdrop-blur-xl">
      <div class="flex items-center gap-3 px-2">
        <div class="grid h-10 w-10 place-items-center rounded-md bg-teal-400 text-lg font-black text-slate-950">✦</div>
        <div>
          <p class="text-[15px] font-semibold tracking-tight text-white">神奇AI助手</p>
          <p class="mt-0.5 text-[11px] uppercase tracking-[.22em] text-muted">Sub2API Desktop</p>
        </div>
      </div>

      <div class="mt-12 px-2 text-[10px] font-semibold uppercase tracking-[.24em] text-muted/70">工作区</div>
      <nav class="mt-3 space-y-1">
        <RouterLink
          v-for="item in navigation"
          :key="item.to"
          :to="item.to"
          class="group flex items-center gap-3 rounded-md px-3 py-3 text-sm text-muted transition hover:bg-white/[.05] hover:text-white"
          active-class="!bg-teal-400/10 !text-teal-300 shadow-[inset_2px_0_0_#2dd4bf]"
        >
          <span class="w-6 font-mono text-[10px] text-muted/60 group-[.router-link-active]:text-teal-300/70">{{ item.mark }}</span>
          <span>{{ item.label }}</span>
        </RouterLink>
      </nav>

      <div class="mt-auto rounded-md border border-white/[.07] bg-white/[.025] p-4">
        <div class="flex items-center gap-2 text-xs text-muted">
          <span class="h-2 w-2 rounded-full" :class="nativeAvailable() ? 'bg-teal-300 shadow-[0_0_10px_#2dd4bf]' : 'bg-amber-300'" />
          {{ nativeAvailable() ? '桌面桥接已就绪' : '浏览器预览模式' }}
        </div>
        <p class="mt-3 text-xs leading-5 text-muted/80">连接密钥默认交给系统安全存储；Codex 不写入密钥，Claude Code 会在确认后写入 settings.json。</p>
      </div>
    </aside>

    <main class="ml-[248px] min-h-screen px-10 py-7">
      <header class="flex items-start justify-between border-b border-white/[.07] pb-6">
        <div>
          <p class="text-[11px] font-semibold uppercase tracking-[.24em] text-teal-300/80">{{ currentEyebrow }}</p>
          <h1 class="mt-2 text-2xl font-semibold tracking-tight text-white">{{ currentTitle }}</h1>
        </div>
        <div class="flex items-center gap-3">
          <span v-if="probeMessage" class="text-xs text-teal-300">{{ probeMessage }}</span>
          <button class="action-secondary" :disabled="!nativeAvailable() || probing" @click="probe">
            {{ probing ? '检查中…' : '检查连接' }}
          </button>
        </div>
      </header>

      <section class="mx-auto max-w-[1180px] py-8">
        <RouterView />
      </section>
    </main>
  </div>
</template>
