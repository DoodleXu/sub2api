<template>
  <div class="flex min-h-screen items-center justify-center bg-gray-50 px-4 py-12 dark:bg-dark-950">
    <main class="w-full max-w-lg rounded-lg border border-gray-200 bg-white p-8 shadow-sm dark:border-dark-700 dark:bg-dark-900">
      <div class="flex flex-col items-center text-center">
        <div
          class="flex h-14 w-14 items-center justify-center rounded-full"
          :class="statusClass.iconWrap"
        >
          <svg
            v-if="status === 'loading'"
            class="h-7 w-7 animate-spin text-primary-600 dark:text-primary-400"
            fill="none"
            viewBox="0 0 24 24"
          >
            <circle
              class="opacity-25"
              cx="12"
              cy="12"
              r="10"
              stroke="currentColor"
              stroke-width="4"
            ></circle>
            <path
              class="opacity-75"
              fill="currentColor"
              d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
            ></path>
          </svg>
          <Icon
            v-else
            :name="status === 'success' ? 'checkCircle' : 'xCircle'"
            size="xl"
            :class="statusClass.icon"
          />
        </div>

        <h1 class="mt-6 text-2xl font-semibold text-gray-900 dark:text-white">
          {{ title }}
        </h1>
        <p class="mt-3 text-sm leading-6 text-gray-600 dark:text-dark-300">
          {{ description }}
        </p>

        <dl
          v-if="result"
          class="mt-6 grid w-full grid-cols-1 gap-3 rounded-md bg-gray-50 p-4 text-left text-sm dark:bg-dark-800"
        >
          <div>
            <dt class="text-gray-500 dark:text-dark-400">Email</dt>
            <dd class="mt-1 break-all font-medium text-gray-900 dark:text-white">
              {{ result.email }}
            </dd>
          </div>
          <div>
            <dt class="text-gray-500 dark:text-dark-400">Notification</dt>
            <dd class="mt-1 break-all font-medium text-gray-900 dark:text-white">
              {{ result.event_label || result.event }}
            </dd>
          </div>
        </dl>

        <router-link
          to="/"
          class="btn btn-primary mt-8"
        >
          返回首页 / Back Home
        </router-link>
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import Icon from '@/components/icons/Icon.vue'
import {
  unsubscribeNotificationEmail,
  type NotificationEmailUnsubscribeResponse,
} from '@/api/auth'
import { extractApiErrorMessage } from '@/utils/apiError'

type Status = 'loading' | 'success' | 'error'

const route = useRoute()
const status = ref<Status>('loading')
const result = ref<NotificationEmailUnsubscribeResponse | null>(null)
const errorMessage = ref('')

const statusClass = computed(() => {
  if (status.value === 'success') {
    return {
      iconWrap: 'bg-emerald-100 dark:bg-emerald-900/30',
      icon: 'text-emerald-600 dark:text-emerald-400',
    }
  }
  if (status.value === 'error') {
    return {
      iconWrap: 'bg-red-100 dark:bg-red-900/30',
      icon: 'text-red-600 dark:text-red-400',
    }
  }
  return {
    iconWrap: 'bg-primary-100 dark:bg-primary-900/30',
    icon: 'text-primary-600 dark:text-primary-400',
  }
})

const title = computed(() => {
  if (status.value === 'success') return '退订成功 / Unsubscribed'
  if (status.value === 'error') return '退订失败 / Unsubscribe failed'
  return '正在处理退订 / Processing unsubscribe'
})

const description = computed(() => {
  if (status.value === 'success') {
    return '后续将不再向该邮箱发送此类可选邮件通知。事务类邮件仍会正常发送。'
  }
  if (status.value === 'error') {
    return errorMessage.value || '退订链接无效或已过期。'
  }
  return '请稍候，我们正在确认退订请求。'
})

onMounted(async () => {
  const token = typeof route.query.token === 'string' ? route.query.token.trim() : ''
  if (!token) {
    status.value = 'error'
    errorMessage.value = '退订链接缺少 token。'
    return
  }
  try {
    result.value = await unsubscribeNotificationEmail(token)
    status.value = 'success'
  } catch (error: unknown) {
    status.value = 'error'
    errorMessage.value = extractApiErrorMessage(error, '退订链接无效或已过期。')
  }
})
</script>
