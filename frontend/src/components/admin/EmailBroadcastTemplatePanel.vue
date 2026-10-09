<template>
  <section class="space-y-4 rounded-xl border border-gray-200 bg-gray-50 p-4 dark:border-dark-600 dark:bg-dark-800" data-testid="broadcast-template-panel">
    <div>
      <h3 class="font-semibold text-gray-900 dark:text-white">{{ t(`${key}.title`) }}</h3>
      <p class="mt-1 text-xs text-gray-500">{{ t(`${key}.hint`) }}</p>
    </div>
    <div class="flex flex-wrap items-end gap-2">
      <div class="min-w-56 flex-1">
        <label for="broadcast-template-select" class="mb-1 block text-sm">{{ t(`${key}.select`) }}</label>
        <select id="broadcast-template-select" v-model="selectedID" class="input" :disabled="busy || loading">
          <option value="">{{ t(`${key}.selectPlaceholder`) }}</option>
          <option v-for="item in templates" :key="item.id" :value="item.id">{{ item.name }}</option>
        </select>
      </div>
      <button class="btn btn-secondary" type="button" :disabled="!selected || busy || !qrValid" @click="applySelected">{{ t(`${key}.apply`) }}</button>
      <button class="btn btn-secondary" type="button" :disabled="loading || busy" @click="loadTemplates">{{ t(`${key}.refresh`) }}</button>
    </div>
    <div v-if="hasWechatQr">
      <label for="broadcast-wechat-qr" class="mb-1 block text-sm">{{ t(`${key}.wechatQr`) }}</label>
      <input id="broadcast-wechat-qr" v-model="wechatQrUrl" type="url" class="input" :placeholder="t(`${key}.wechatQrPlaceholder`)" :aria-invalid="!qrValid" @input="updateWechatQr" />
      <p class="mt-1 text-xs text-gray-500">{{ t(`${key}.wechatQrHint`) }}</p>
      <p v-if="!qrValid" role="alert" class="mt-1 text-sm text-red-600">{{ t(`${key}.wechatQrInvalid`) }}</p>
    </div>
    <div class="flex flex-wrap items-end gap-2">
      <div class="min-w-56 flex-1">
        <label for="broadcast-template-name" class="mb-1 block text-sm">{{ t(`${key}.name`) }}</label>
        <input id="broadcast-template-name" v-model="name" class="input" maxlength="100" :placeholder="t(`${key}.namePlaceholder`)" />
      </div>
      <button class="btn btn-secondary" type="button" :disabled="!canSave || busy" @click="save(false)">{{ t(`${key}.saveNew`) }}</button>
      <button class="btn btn-secondary" type="button" :disabled="!canSave || !isCustom || busy" @click="save(true)">{{ t(`${key}.update`) }}</button>
      <button class="btn btn-secondary" type="button" :disabled="!isCustom || busy" @click="deleteTarget = selected || null">{{ t(`${key}.delete`) }}</button>
    </div>
    <div class="flex flex-wrap items-center justify-between gap-2 border-t border-gray-200 pt-4 dark:border-dark-600">
      <h3 class="font-semibold">{{ t(`${key}.preview`) }}</h3>
      <div class="flex gap-2">
        <button type="button" class="btn btn-secondary btn-sm" :aria-pressed="mobile" @click="mobile = !mobile">{{ t(mobile ? `${key}.desktop` : `${key}.mobile`) }}</button>
        <button type="button" class="btn btn-primary btn-sm" :disabled="!hasContent || previewLoading" @click="loadPreview">{{ t(previewLoading ? `${key}.previewLoading` : `${key}.refreshPreview`) }}</button>
      </div>
    </div>
    <p class="text-xs text-gray-500">{{ t(`${key}.previewHint`) }}</p>
    <p v-if="previewError" role="alert" class="text-sm text-red-600">{{ previewError }}</p>
    <div v-if="preview" class="space-y-3">
      <p class="text-sm break-words">{{ t(`${key}.subject`) }}：{{ preview.subject }}</p>
      <div class="overflow-x-auto rounded-lg border border-gray-200 bg-white p-2">
        <iframe :srcdoc="previewDocument" sandbox="" referrerpolicy="no-referrer" :title="t(`${key}.preview`)" class="mx-auto block h-[640px] max-w-full border-0" :style="{ width: mobile ? '375px' : '100%' }" />
      </div>
    </div>
    <p v-else class="rounded-lg border border-dashed border-gray-300 p-6 text-center text-sm text-gray-500">{{ t(`${key}.previewEmpty`) }}</p>
    <ConfirmDialog :show="deleteTarget !== null" :title="t(`${key}.delete`)" :message="t(`${key}.deleteConfirm`, { name: deleteTarget?.name || '' })" :danger="true" @confirm="remove" @cancel="deleteTarget = null" />
    <TotpStepUpDialog :controller="stepUp" />
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { EmailBroadcastContent, EmailBroadcastTemplate } from '@/api/admin/settings'
import { rechargeBonusTemplate, getWechatQrUrl, isValidWechatQrUrl, replaceWechatQrUrl } from '@/utils/emailBroadcastTemplates'
import { createEmailPreviewDocument } from '@/utils/emailPreview'
import { useAppStore } from '@/stores'
import { useStepUp, isStepUpCancelled, isStepUpBlocked } from '@/composables/useStepUp'
import { extractApiErrorMessage } from '@/utils/apiError'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'

const props = defineProps<{ content: EmailBroadcastContent }>()
const emit = defineEmits<{ apply: [content: EmailBroadcastContent]; validity: [valid: boolean] }>()
const { t } = useI18n()
const key = 'admin.settings.emailBroadcast.templates'
const store = useAppStore()
const stepUp = useStepUp()
const customTemplates = ref<EmailBroadcastTemplate[]>([])
const selectedID = ref('')
const name = ref('')
const wechatQrUrl = ref('')
const busy = ref(false)
const loading = ref(false)
const mobile = ref(false)
const previewLoading = ref(false)
const previewError = ref('')
const preview = ref<{ subject: string; html: string } | null>(null)
const deleteTarget = ref<EmailBroadcastTemplate | null>(null)
let previewRequest = 0
const templates = computed(() => [rechargeBonusTemplate, ...customTemplates.value])
const selected = computed(() => templates.value.find(item => item.id === selectedID.value))
const isCustom = computed(() => !!selected.value && selected.value.id !== rechargeBonusTemplate.id)
const hasWechatQr = computed(() => getWechatQrUrl(props.content.message_html) !== null || getWechatQrUrl(selected.value?.message_html || '') !== null)
const qrValid = computed(() => !hasWechatQr.value || isValidWechatQrUrl(wechatQrUrl.value.trim()))
const hasContent = computed(() => !!props.content.message_title.trim() && !!props.content.message_html.trim() && qrValid.value)
const canSave = computed(() => hasContent.value && !!name.value.trim())
const previewDocument = computed(() => createEmailPreviewDocument(preview.value?.html || ''))

function report(error: unknown) {
  if (isStepUpCancelled(error)) return
  store.showError(isStepUpBlocked(error) ? t('stepUp.notEnabled') : extractApiErrorMessage(error, t(`${key}.failed`)))
}
async function loadTemplates() {
  loading.value = true
  try { customTemplates.value = await adminAPI.settings.listEmailBroadcastTemplates() }
  catch (error) { report(error) }
  finally { loading.value = false }
}
function applySelected() {
  if (!selected.value || !qrValid.value) return
  const { locale, message_title, message_html, action_label, action_url } = selected.value
  emit('apply', { locale, message_title, message_html: getWechatQrUrl(message_html) !== null ? replaceWechatQrUrl(message_html, wechatQrUrl.value.trim()) : message_html, action_label, action_url })
}
function updateWechatQr() {
  if (!qrValid.value || getWechatQrUrl(props.content.message_html) === null) return
  emit('apply', { ...props.content, message_html: replaceWechatQrUrl(props.content.message_html, wechatQrUrl.value.trim()) })
}
async function save(update: boolean) {
  busy.value = true
  const id = update ? selected.value?.id : undefined
  const content = { ...props.content, name: name.value.trim() }
  try {
    const saved = await stepUp.run(() => adminAPI.settings.saveEmailBroadcastTemplate(content, id))
    customTemplates.value = [saved, ...customTemplates.value.filter(item => item.id !== saved.id)]
    selectedID.value = saved.id
    store.showSuccess(t(`${key}.saved`))
  } catch (error) { report(error) }
  finally { busy.value = false }
}
async function remove() {
  const id = deleteTarget.value?.id
  deleteTarget.value = null
  if (!id) return
  busy.value = true
  try {
    await stepUp.run(() => adminAPI.settings.deleteEmailBroadcastTemplate(id))
    customTemplates.value = customTemplates.value.filter(item => item.id !== id)
    if (selectedID.value === id) selectedID.value = ''
    store.showSuccess(t(`${key}.deleted`))
  } catch (error) { report(error) }
  finally { busy.value = false }
}
async function loadPreview() {
  const request = ++previewRequest
  previewLoading.value = true
  previewError.value = ''
  try {
    const result = await adminAPI.settings.previewEmailBroadcastContent({ ...props.content })
    if (request === previewRequest) preview.value = result
  } catch (error) {
    if (request === previewRequest) previewError.value = extractApiErrorMessage(error, t(`${key}.failed`))
  } finally { if (request === previewRequest) previewLoading.value = false }
}
watch(selectedID, () => {
  name.value = selected.value?.name || ''
  wechatQrUrl.value = getWechatQrUrl(selected.value?.message_html || '') ?? getWechatQrUrl(props.content.message_html) ?? ''
})
watch(() => props.content.message_html, html => {
  const url = getWechatQrUrl(html)
  if (url !== null) wechatQrUrl.value = url
}, { immediate: true })
watch(qrValid, valid => emit('validity', valid), { immediate: true })
function invalidatePreview() {
  ++previewRequest
  preview.value = null
  previewError.value = ''
  previewLoading.value = false
}
watch(wechatQrUrl, invalidatePreview)
watch(() => props.content, invalidatePreview, { deep: true })
onMounted(loadTemplates)
</script>
