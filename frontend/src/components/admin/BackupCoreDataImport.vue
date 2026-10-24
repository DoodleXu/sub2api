<template>
  <div class="card p-6">
    <div class="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div>
        <h3 class="text-base font-semibold text-gray-900 dark:text-white">
          {{ t('admin.backup.coreData.title') }}
        </h3>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.backup.coreData.description') }}
        </p>
      </div>
      <div class="flex flex-wrap gap-2">
        <button type="button" class="btn btn-secondary btn-sm" :disabled="exportingCoreData" @click="exportCoreData">
          {{ exportingCoreData ? t('common.loading') : t('admin.backup.coreData.exportButton') }}
        </button>
      </div>
    </div>

    <div class="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(18rem,24rem)]">
      <div class="space-y-3">
        <div class="rounded-lg border border-blue-200 bg-blue-50 p-3 text-sm text-blue-700 dark:border-blue-800 dark:bg-blue-900/20 dark:text-blue-300">
          {{ t('admin.backup.coreData.scopeHint') }}
        </div>
        <div class="rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-700 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-300">
          {{ t('admin.backup.coreData.warning') }}
        </div>

        <div
          v-if="coreDataImportResult"
          class="space-y-2 rounded-lg border border-gray-200 p-4 dark:border-dark-700"
        >
          <div class="text-sm font-medium text-gray-900 dark:text-white">
            {{ t('admin.backup.coreData.resultTitle') }}
          </div>
          <div class="text-sm text-gray-700 dark:text-dark-300">
            {{ t('admin.backup.coreData.resultSummary', coreDataImportResult) }}
          </div>
          <div v-if="coreDataImportErrors.length" class="mt-2 max-h-44 overflow-auto rounded-lg bg-gray-50 p-3 font-mono text-xs dark:bg-dark-800">
            <div v-for="(item, idx) in coreDataImportErrors" :key="idx" class="whitespace-pre-wrap">
              {{ item.kind }} {{ item.name || item.proxy_key || '-' }} - {{ item.message }}
            </div>
          </div>
        </div>
      </div>

      <form id="core-data-import-form" class="space-y-3" @submit.prevent="importCoreData">
        <div>
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.backup.coreData.fileLabel') }}
          </label>
          <div class="rounded-lg border border-dashed border-gray-300 bg-gray-50 px-4 py-3 dark:border-dark-600 dark:bg-dark-800">
            <div class="min-w-0">
              <div class="truncate text-sm text-gray-700 dark:text-dark-200">
                {{ coreDataFileName || t('admin.backup.coreData.noFile') }}
              </div>
              <div class="text-xs text-gray-500 dark:text-dark-400">
                JSON (.json)
              </div>
            </div>
            <div class="mt-3 flex flex-wrap gap-2">
              <button type="button" class="btn btn-secondary btn-sm" @click="openCoreDataFilePicker">
                {{ t('common.chooseFile') }}
              </button>
              <button type="submit" class="btn btn-primary btn-sm" :disabled="importingCoreData">
                {{ importingCoreData ? t('admin.backup.coreData.importing') : t('admin.backup.coreData.importButton') }}
              </button>
            </div>
          </div>
          <input
            ref="coreDataFileInput"
            type="file"
            class="hidden"
            accept="application/json,.json"
            @change="handleCoreDataFileChange"
          />
        </div>
      </form>
    </div>
  </div>
  <TotpStepUpDialog :controller="backupStepUp" />
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api'
import { useAppStore } from '@/stores'
import type { AdminDataImportResult } from '@/types'
import { useStepUp, isStepUpBlocked, isStepUpCancelled, stepUpBlockReason } from '@/composables/useStepUp'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'

const { t } = useI18n()
const appStore = useAppStore()
const backupStepUp = useStepUp()

const coreDataFileInput = ref<HTMLInputElement | null>(null)
const coreDataFile = ref<File | null>(null)
const coreDataImportResult = ref<AdminDataImportResult | null>(null)
const importingCoreData = ref(false)
const exportingCoreData = ref(false)
const coreDataFileName = computed(() => coreDataFile.value?.name || '')
const coreDataImportErrors = computed(() => coreDataImportResult.value?.errors || [])

function reportStepUpBlocked(error: unknown): boolean {
  if (!isStepUpBlocked(error)) return false
  appStore.showError(
    stepUpBlockReason(error) === 'STEP_UP_ADMIN_API_KEY_FORBIDDEN'
      ? t('stepUp.adminApiKeyForbidden')
      : t('stepUp.notEnabled')
  )
  return true
}

function openCoreDataFilePicker() {
  coreDataFileInput.value?.click()
}

function handleCoreDataFileChange(event: Event) {
  const target = event.target as HTMLInputElement
  coreDataFile.value = target.files?.[0] || null
  coreDataImportResult.value = null
}

async function readFileAsText(sourceFile: File): Promise<string> {
  if (typeof sourceFile.text === 'function') {
    return sourceFile.text()
  }

  if (typeof sourceFile.arrayBuffer === 'function') {
    const buffer = await sourceFile.arrayBuffer()
    return new TextDecoder().decode(buffer)
  }

  return await new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result ?? ''))
    reader.onerror = () => reject(reader.error || new Error('Failed to read file'))
    reader.readAsText(sourceFile)
  })
}

function downloadJSONFile(payload: unknown, fileName: string) {
  const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = fileName
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  URL.revokeObjectURL(url)
}

async function exportCoreData() {
  exportingCoreData.value = true
  try {
    const payload = await backupStepUp.run(() => adminAPI.accounts.exportData({ includeProxies: true, includeArchived: true }))
    downloadJSONFile(payload, `sub2api-core-data-${new Date().toISOString().slice(0, 10)}.json`)
    appStore.showSuccess(t('admin.backup.coreData.exportSuccess'))
  } catch (error) {
    if (!isStepUpCancelled(error) && !reportStepUpBlocked(error)) {
      appStore.showError((error as { message?: string })?.message || t('admin.backup.coreData.exportFailed'))
    }
  } finally {
    exportingCoreData.value = false
  }
}

async function importCoreData() {
  if (!coreDataFile.value) {
    appStore.showError(t('admin.backup.coreData.noFile'))
    return
  }

  importingCoreData.value = true
  try {
    const text = await readFileAsText(coreDataFile.value)
    const payload = JSON.parse(text)
    const result = await adminAPI.accounts.importData({
      data: payload,
      skip_default_group_bind: true
    })
    coreDataImportResult.value = result

    const hasErrors = result.account_failed > 0 || result.proxy_failed > 0
    const messageParams: Record<string, unknown> = {
      account_created: result.account_created,
      account_failed: result.account_failed,
      proxy_created: result.proxy_created,
      proxy_reused: result.proxy_reused,
      proxy_failed: result.proxy_failed
    }

    if (hasErrors) {
      appStore.showError(t('admin.backup.coreData.importCompletedWithErrors', messageParams))
    } else {
      appStore.showSuccess(t('admin.backup.coreData.importSuccess', messageParams))
    }
  } catch (error: any) {
    if (error instanceof SyntaxError) {
      appStore.showError(t('admin.backup.coreData.parseFailed'))
    } else {
      appStore.showError(error?.message || t('admin.backup.coreData.importFailed'))
    }
  } finally {
    importingCoreData.value = false
  }
}
</script>
