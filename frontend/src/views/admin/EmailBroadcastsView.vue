<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-6">
      <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <div>
          <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">
            {{ t("admin.settings.emailBroadcast.title") }}
          </h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
            {{ t("admin.settings.emailBroadcast.pageDescription") }}
          </p>
        </div>
        <button
          type="button"
          class="btn btn-secondary"
          :disabled="emailBroadcastTasksLoading"
          @click="loadEmailBroadcastTasks"
        >
          <Icon
            name="refresh"
            size="sm"
            :class="emailBroadcastTasksLoading && 'animate-spin'"
          />
          {{ t("admin.settings.emailBroadcast.refreshTasks") }}
        </button>
      </div>

      <div class="card">
        <div
          class="flex flex-col gap-3 border-b border-gray-100 px-6 py-4 dark:border-dark-700 md:flex-row md:items-center md:justify-between"
        >
          <div>
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
              {{ t("admin.settings.emailBroadcast.composeTitle") }}
            </h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.emailBroadcast.description") }}
            </p>
          </div>
          <div
            class="rounded-md border border-blue-100 bg-blue-50 px-3 py-2 text-xs text-blue-700 dark:border-blue-900/50 dark:bg-blue-950/30 dark:text-blue-300"
          >
            {{
              t("admin.settings.emailBroadcast.rateHint", {
                seconds: emailBroadcastEstimatedInterval,
              })
            }}
          </div>
        </div>
        <div class="space-y-5 px-6 py-6">
          <div
            class="flex flex-col gap-3 rounded-md border border-gray-100 bg-gray-50 px-3 py-3 dark:border-dark-700 dark:bg-dark-800/60 md:flex-row md:items-center md:justify-between"
          >
            <div class="text-sm text-gray-600 dark:text-gray-300">
              {{
                emailBroadcastDraftSavedAt
                  ? t("admin.settings.emailBroadcast.draftSavedAt", {
                      time: formatEmailBroadcastDate(emailBroadcastDraftSavedAt),
                    })
                  : t("admin.settings.emailBroadcast.noDraft")
              }}
            </div>
            <div class="flex flex-wrap gap-2">
              <button
                type="button"
                class="btn btn-secondary btn-sm"
                :disabled="emailBroadcastDraftLoading"
                @click="loadEmailBroadcastDraft(true)"
              >
                <Icon
                  name="refresh"
                  size="sm"
                  :class="emailBroadcastDraftLoading && 'animate-spin'"
                />
                {{ t("admin.settings.emailBroadcast.loadDraft") }}
              </button>
              <button
                type="button"
                class="btn btn-secondary btn-sm"
                :disabled="emailBroadcastDraftSaving"
                @click="saveEmailBroadcastDraft"
              >
                <span
                  v-if="emailBroadcastDraftSaving"
                  class="h-4 w-4 animate-spin rounded-full border-b-2 border-current"
                ></span>
                <Icon v-else name="save" size="sm" />
                {{ t("admin.settings.emailBroadcast.saveDraft") }}
              </button>
              <button
                type="button"
                class="btn btn-secondary btn-sm"
                :disabled="emailBroadcastDraftSaving || !emailBroadcastDraftSavedAt"
                @click="() => clearEmailBroadcastDraft()"
              >
                <Icon name="trash" size="sm" />
                {{ t("admin.settings.emailBroadcast.clearDraft") }}
              </button>
            </div>
          </div>

          <div class="grid gap-4 md:grid-cols-3">
            <div>
              <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
                {{ t("admin.settings.emailBroadcast.scope") }}
              </label>
              <Select
                v-model="emailBroadcastForm.scope"
                :options="emailBroadcastScopeOptions"
              />
            </div>
            <div>
              <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
                {{ t("admin.settings.emailBroadcast.locale") }}
              </label>
              <Select
                v-model="emailBroadcastForm.locale"
                :options="emailBroadcastLocaleOptions"
              />
            </div>
            <div>
              <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
                {{ t("admin.settings.emailBroadcast.rpm") }}
              </label>
              <input
                v-model.number="emailBroadcastForm.rpm"
                type="number"
                min="1"
                max="30"
                class="input"
              />
            </div>
          </div>

          <div
            v-if="emailBroadcastForm.scope === 'custom'"
            class="grid gap-4 md:grid-cols-2"
          >
            <div>
              <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
                {{ t("admin.settings.emailBroadcast.customUserIds") }}
              </label>
              <textarea
                v-model="emailBroadcastCustomUserIDsInput"
                rows="3"
                class="input min-h-[84px]"
                :placeholder="t('admin.settings.emailBroadcast.customUserIdsPlaceholder')"
              ></textarea>
            </div>
            <div>
              <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
                {{ t("admin.settings.emailBroadcast.customEmails") }}
              </label>
              <textarea
                v-model="emailBroadcastCustomEmailsInput"
                rows="3"
                class="input min-h-[84px]"
                :placeholder="t('admin.settings.emailBroadcast.customEmailsPlaceholder')"
              ></textarea>
            </div>
          </div>

          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.settings.emailBroadcast.messageTitle") }}
            </label>
            <input
              v-model="emailBroadcastForm.message_title"
              type="text"
              maxlength="200"
              class="input"
              :placeholder="t('admin.settings.emailBroadcast.messageTitlePlaceholder')"
            />
          </div>

          <div>
            <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t("admin.settings.emailBroadcast.messageHtml") }}
            </label>
            <textarea
              v-model="emailBroadcastForm.message_html"
              rows="7"
              class="input min-h-[180px] font-mono text-sm"
              :placeholder="t('admin.settings.emailBroadcast.messageHtmlPlaceholder')"
            ></textarea>
          </div>

          <div class="grid gap-4 md:grid-cols-2">
            <div>
              <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
                {{ t("admin.settings.emailBroadcast.actionLabel") }}
              </label>
              <input
                v-model="emailBroadcastForm.action_label"
                type="text"
                class="input"
                :placeholder="t('admin.settings.emailBroadcast.actionLabelPlaceholder')"
              />
            </div>
            <div>
              <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
                {{ t("admin.settings.emailBroadcast.actionUrl") }}
              </label>
              <input
                v-model="emailBroadcastForm.action_url"
                type="text"
                class="input"
                placeholder="/notice"
              />
            </div>
          </div>

          <div class="flex justify-end">
            <button
              type="button"
              class="btn btn-primary"
              :disabled="!canSendEmailBroadcast"
              @click="requestEmailBroadcastConfirmation"
            >
              <span
                v-if="emailBroadcastSending"
                class="h-4 w-4 animate-spin rounded-full border-b-2 border-current"
              ></span>
              <Icon v-else name="mail" size="sm" />
              {{
                emailBroadcastSending
                  ? t("admin.settings.emailBroadcast.sending")
                  : t("admin.settings.emailBroadcast.send")
              }}
            </button>
          </div>
        </div>
      </div>

      <div class="card">
        <div
          class="flex flex-col gap-3 border-b border-gray-100 px-6 py-4 dark:border-dark-700 md:flex-row md:items-center md:justify-between"
        >
          <div>
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
              {{ t("admin.settings.emailBroadcast.taskTitle") }}
            </h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              {{ t("admin.settings.emailBroadcast.taskDescription") }}
            </p>
          </div>
          <div
            v-if="activeBatchId"
            class="rounded-md border border-amber-100 bg-amber-50 px-3 py-2 text-xs text-amber-700 dark:border-amber-900/50 dark:bg-amber-950/30 dark:text-amber-300"
          >
            {{ t("admin.settings.emailBroadcast.activeBatch", { batch: activeBatchId }) }}
          </div>
        </div>

        <div class="p-6">
          <div
            v-if="emailBroadcastTasksLoading && emailBroadcastTasks.length === 0"
            class="flex items-center justify-center py-10"
          >
            <div class="h-7 w-7 animate-spin rounded-full border-b-2 border-primary-600"></div>
          </div>

          <div
            v-else-if="emailBroadcastTasks.length === 0"
            class="rounded-lg border border-dashed border-gray-200 py-10 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-gray-400"
          >
            {{ t("admin.settings.emailBroadcast.noTasks") }}
          </div>

          <div v-else class="overflow-x-auto">
            <table class="min-w-full divide-y divide-gray-200 dark:divide-dark-700">
              <thead class="bg-gray-50 dark:bg-dark-800">
                <tr>
                  <th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                    {{ t("admin.settings.emailBroadcast.task") }}
                  </th>
                  <th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                    {{ t("admin.settings.emailBroadcast.progress") }}
                  </th>
                  <th class="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                    {{ t("admin.settings.emailBroadcast.startedAt") }}
                  </th>
                  <th class="px-4 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                    {{ t("common.actions") }}
                  </th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 bg-white dark:divide-dark-700 dark:bg-dark-900">
                <tr v-for="task in emailBroadcastTasks" :key="task.batch_id">
                  <td class="px-4 py-4 align-top">
                    <div class="flex flex-col gap-2">
                      <div class="flex flex-wrap items-center gap-2">
                        <span :class="['badge text-xs', emailBroadcastStatusBadgeClass(task.status)]">
                          {{ emailBroadcastStatusLabel(task.status) }}
                        </span>
                        <span class="font-medium text-gray-900 dark:text-white">
                          {{ task.message_title || task.batch_id }}
                        </span>
                      </div>
                      <div class="font-mono text-xs text-gray-500 dark:text-gray-400">
                        {{ task.batch_id }}
                      </div>
                      <div
                        v-if="task.last_error"
                        class="max-w-lg text-xs text-red-600 dark:text-red-300"
                      >
                        {{ task.last_error }}
                      </div>
                    </div>
                  </td>
                  <td class="min-w-[260px] px-4 py-4 align-top">
                    <div class="mb-2 flex items-center justify-between text-xs text-gray-500 dark:text-gray-400">
                      <span>
                        {{
                          t("admin.settings.emailBroadcast.statusSummary", {
                            status: emailBroadcastStatusLabel(task.status),
                            sent: task.sent_count,
                            skipped: task.skipped_count,
                            unsubscribed: task.unsubscribed_count,
                            failed: task.failure_count,
                            total: task.target_count,
                          })
                        }}
                      </span>
                      <span>{{ emailBroadcastProgress(task) }}%</span>
                    </div>
                    <div class="h-2 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
                      <div
                        class="h-full rounded-full bg-primary-500 transition-all"
                        :style="{ width: `${emailBroadcastProgress(task)}%` }"
                      ></div>
                    </div>
                  </td>
                  <td class="px-4 py-4 align-top text-sm text-gray-600 dark:text-gray-300">
                    <div>{{ formatEmailBroadcastDate(task.started_at) }}</div>
                    <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                      {{ t("admin.settings.emailBroadcast.updatedAt") }}:
                      {{ formatEmailBroadcastDate(task.updated_at) }}
                    </div>
                  </td>
                  <td class="px-4 py-4 align-top">
                    <div class="flex justify-end gap-2">
                      <button
                        v-if="canCancelEmailBroadcast(task)"
                        type="button"
                        class="btn btn-secondary btn-sm"
                        :disabled="emailBroadcastOperatingBatch === task.batch_id"
                        @click="cancelEmailBroadcastTask(task.batch_id)"
                      >
                        <Icon name="ban" size="sm" />
                        {{ t("admin.settings.emailBroadcast.cancel") }}
                      </button>
                      <button
                        v-if="canResumeEmailBroadcast(task)"
                        type="button"
                        class="btn btn-secondary btn-sm"
                        :disabled="emailBroadcastOperatingBatch === task.batch_id"
                        @click="resumeEmailBroadcastTask(task.batch_id, 'remaining')"
                      >
                        <Icon name="play" size="sm" />
                        {{ t("admin.settings.emailBroadcast.resume") }}
                      </button>
                      <button
                        v-if="canResendFailedEmailBroadcast(task)"
                        type="button"
                        class="btn btn-secondary btn-sm"
                        :disabled="emailBroadcastOperatingBatch === task.batch_id"
                        @click="resumeEmailBroadcastTask(task.batch_id, 'failed')"
                      >
                        <Icon name="refresh" size="sm" />
                        {{ t("admin.settings.emailBroadcast.resendFailed") }}
                      </button>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>

      <ConfirmDialog
        :show="emailBroadcastConfirmDialog.show"
        :title="t('admin.settings.emailBroadcast.confirmTitle')"
        :message="emailBroadcastConfirmDialog.message"
        :confirm-text="t('admin.settings.emailBroadcast.confirmSend')"
        :confirm-disabled="!canConfirmEmailBroadcast"
        variant="warning"
        @confirm="handleEmailBroadcastConfirm"
        @cancel="cancelEmailBroadcastConfirm"
      >
        <div class="space-y-3 text-sm text-gray-700 dark:text-gray-300">
          <div>{{ emailBroadcastConfirmDialog.summary }}</div>
          <div v-if="emailBroadcastConfirmDialog.requiresPhrase">
            <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
              {{ t("admin.settings.emailBroadcast.confirmPhraseLabel") }}
            </label>
            <input
              v-model="emailBroadcastConfirmDialog.phrase"
              type="text"
              class="input"
              :placeholder="emailBroadcastConfirmPhrase"
            />
          </div>
        </div>
      </ConfirmDialog>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { adminAPI } from "@/api/admin";
import type {
  EmailBroadcastScope,
  EmailBroadcastDraftResponse,
  EmailBroadcastStatusResponse,
  ResumeEmailBroadcastRequest,
  SendEmailBroadcastRequest,
} from "@/api/admin/settings";
import AppLayout from "@/components/layout/AppLayout.vue";
import Select from "@/components/common/Select.vue";
import ConfirmDialog from "@/components/common/ConfirmDialog.vue";
import Icon from "@/components/icons/Icon.vue";
import { useAppStore } from "@/stores";
import { extractApiErrorMessage } from "@/utils/apiError";

const { t } = useI18n();
const appStore = useAppStore();

const emailBroadcastSending = ref(false);
const emailBroadcastTasksLoading = ref(false);
const emailBroadcastTasks = ref<EmailBroadcastStatusResponse[]>([]);
const activeBatchId = ref("");
const emailBroadcastDraftLoading = ref(false);
const emailBroadcastDraftSaving = ref(false);
const emailBroadcastDraftSavedAt = ref("");
const emailBroadcastOperatingBatch = ref<string | null>(null);
const emailBroadcastCustomEmailsInput = ref("");
const emailBroadcastCustomUserIDsInput = ref("");
let emailBroadcastStatusTimer: number | null = null;

const emailBroadcastForm = reactive({
  scope: "active_users" as EmailBroadcastScope,
  locale: "zh",
  message_title: "",
  message_html: "",
  action_label: "",
  action_url: "",
  rpm: 6,
});

const emailBroadcastConfirmPhrase = "SEND";
const emailBroadcastConfirmDialog = reactive<{
  show: boolean;
  message: string;
  summary: string;
  phrase: string;
  requiresPhrase: boolean;
  payload: SendEmailBroadcastRequest | null;
}>({
  show: false,
  message: "",
  summary: "",
  phrase: "",
  requiresPhrase: false,
  payload: null,
});

const emailBroadcastScopeOptions = computed(() => [
  {
    value: "active_users",
    label: t("admin.settings.emailBroadcast.scopes.activeUsers"),
  },
  {
    value: "all_users",
    label: t("admin.settings.emailBroadcast.scopes.allUsers"),
  },
  {
    value: "admins",
    label: t("admin.settings.emailBroadcast.scopes.admins"),
  },
  {
    value: "custom",
    label: t("admin.settings.emailBroadcast.scopes.custom"),
  },
]);

const emailBroadcastLocaleOptions = computed(() => [
  { value: "zh", label: t("admin.settings.emailBroadcast.locales.zh") },
  { value: "en", label: t("admin.settings.emailBroadcast.locales.en") },
]);

const emailBroadcastEstimatedInterval = computed(() => {
  const rpm = Math.max(1, Number(emailBroadcastForm.rpm) || 6);
  return Math.ceil(60 / rpm);
});

const canSendEmailBroadcast = computed(
  () =>
    emailBroadcastForm.message_title.trim() !== "" &&
    emailBroadcastForm.message_html.trim() !== "" &&
    !emailBroadcastSending.value,
);

const canConfirmEmailBroadcast = computed(
  () =>
    !emailBroadcastConfirmDialog.requiresPhrase ||
    emailBroadcastConfirmDialog.phrase.trim() === emailBroadcastConfirmPhrase,
);

function splitNotificationInput(value: string): string[] {
  const seen = new Set<string>();
  return value
    .split(/[\n,]+/)
    .map((item) => item.trim())
    .filter((item) => {
      if (!item) return false;
      const key = item.toLowerCase();
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
}

function splitEmailBroadcastUserIDs(value: string): number[] {
  const seen = new Set<number>();
  return value
    .split(/[\n,]+/)
    .map((item) => Number.parseInt(item.trim(), 10))
    .filter((item) => {
      if (!Number.isFinite(item) || item <= 0 || seen.has(item)) {
        return false;
      }
      seen.add(item);
      return true;
    });
}

function buildEmailBroadcastPayload(): SendEmailBroadcastRequest {
  return {
    scope: emailBroadcastForm.scope,
    locale: emailBroadcastForm.locale,
    message_title: emailBroadcastForm.message_title.trim(),
    message_html: emailBroadcastForm.message_html.trim(),
    action_label: emailBroadcastForm.action_label.trim() || undefined,
    action_url: emailBroadcastForm.action_url.trim() || undefined,
    user_ids:
      emailBroadcastForm.scope === "custom"
        ? splitEmailBroadcastUserIDs(emailBroadcastCustomUserIDsInput.value)
        : undefined,
    emails:
      emailBroadcastForm.scope === "custom"
        ? splitNotificationInput(emailBroadcastCustomEmailsInput.value)
        : undefined,
    rpm: Math.max(1, Math.min(30, Number(emailBroadcastForm.rpm) || 6)),
  };
}

function applyEmailBroadcastDraft(draft: EmailBroadcastDraftResponse): void {
  emailBroadcastForm.scope = draft.scope;
  emailBroadcastForm.locale = draft.locale;
  emailBroadcastForm.message_title = draft.message_title;
  emailBroadcastForm.message_html = draft.message_html;
  emailBroadcastForm.action_label = draft.action_label || "";
  emailBroadcastForm.action_url = draft.action_url || "";
  emailBroadcastForm.rpm = draft.rpm;
  emailBroadcastCustomUserIDsInput.value = (draft.user_ids || []).join("\n");
  emailBroadcastCustomEmailsInput.value = (draft.emails || []).join("\n");
  emailBroadcastDraftSavedAt.value = draft.saved_at || "";
}

function emailBroadcastScopeLabel(scope: EmailBroadcastScope): string {
  const option = emailBroadcastScopeOptions.value.find((item) => item.value === scope);
  return option?.label || scope;
}

function requestEmailBroadcastConfirmation(): void {
  const payload = buildEmailBroadcastPayload();
  if (!payload.message_title || !payload.message_html) {
    appStore.showError(t("admin.settings.emailBroadcast.required"));
    return;
  }
  if (
    payload.scope === "custom" &&
    (payload.user_ids?.length || 0) === 0 &&
    (payload.emails?.length || 0) === 0
  ) {
    appStore.showError(t("admin.settings.emailBroadcast.customRequired"));
    return;
  }
  const requiresPhrase = payload.scope !== "custom";
  emailBroadcastConfirmDialog.payload = payload;
  emailBroadcastConfirmDialog.requiresPhrase = requiresPhrase;
  emailBroadcastConfirmDialog.phrase = "";
  emailBroadcastConfirmDialog.message = t("admin.settings.emailBroadcast.confirmMessage");
  emailBroadcastConfirmDialog.summary = t("admin.settings.emailBroadcast.confirmSummary", {
    scope: emailBroadcastScopeLabel(payload.scope),
    rpm: payload.rpm,
    seconds: Math.ceil(60 / payload.rpm),
  });
  emailBroadcastConfirmDialog.show = true;
}

async function handleEmailBroadcastConfirm(): Promise<void> {
  if (
    emailBroadcastConfirmDialog.requiresPhrase &&
    emailBroadcastConfirmDialog.phrase.trim() !== emailBroadcastConfirmPhrase
  ) {
    appStore.showError(
      t("admin.settings.emailBroadcast.confirmPhraseRequired", {
        phrase: emailBroadcastConfirmPhrase,
      }),
    );
    return;
  }
  const payload = emailBroadcastConfirmDialog.payload;
  emailBroadcastConfirmDialog.show = false;
  emailBroadcastConfirmDialog.payload = null;
  if (!payload) return;
  await sendEmailBroadcast(payload);
}

function cancelEmailBroadcastConfirm(): void {
  emailBroadcastConfirmDialog.show = false;
  emailBroadcastConfirmDialog.payload = null;
}

async function sendEmailBroadcast(payload: SendEmailBroadcastRequest): Promise<void> {
  emailBroadcastSending.value = true;
  try {
    const result = await adminAPI.settings.sendEmailBroadcast(payload);
    await loadEmailBroadcastTasks();
    await clearEmailBroadcastDraft(false);
    scheduleEmailBroadcastStatusRefresh();
    appStore.showSuccess(
      t("admin.settings.emailBroadcast.started", {
        count: result.target_count,
        batch: result.batch_id,
      }),
    );
  } catch (error: unknown) {
    appStore.showError(
      extractApiErrorMessage(error, t("admin.settings.emailBroadcast.failed")),
    );
  } finally {
    emailBroadcastSending.value = false;
  }
}

async function loadEmailBroadcastDraft(showError = false): Promise<void> {
  emailBroadcastDraftLoading.value = true;
  try {
    const draft = await adminAPI.settings.getEmailBroadcastDraft();
    if (draft) {
      applyEmailBroadcastDraft(draft);
    } else if (showError) {
      appStore.showError(t("admin.settings.emailBroadcast.noDraft"));
    }
  } catch (error: unknown) {
    if (showError) {
      appStore.showError(
        extractApiErrorMessage(error, t("admin.settings.emailBroadcast.draftLoadFailed")),
      );
    }
  } finally {
    emailBroadcastDraftLoading.value = false;
  }
}

async function saveEmailBroadcastDraft(): Promise<void> {
  emailBroadcastDraftSaving.value = true;
  try {
    const result = await adminAPI.settings.saveEmailBroadcastDraft(buildEmailBroadcastPayload());
    applyEmailBroadcastDraft(result);
    appStore.showSuccess(t("admin.settings.emailBroadcast.draftSaved"));
  } catch (error: unknown) {
    appStore.showError(
      extractApiErrorMessage(error, t("admin.settings.emailBroadcast.draftSaveFailed")),
    );
  } finally {
    emailBroadcastDraftSaving.value = false;
  }
}

async function clearEmailBroadcastDraft(showNotice = true): Promise<void> {
  try {
    await adminAPI.settings.deleteEmailBroadcastDraft();
    emailBroadcastDraftSavedAt.value = "";
    if (showNotice) {
      appStore.showSuccess(t("admin.settings.emailBroadcast.draftCleared"));
    }
  } catch (error: unknown) {
    if (showNotice) {
      appStore.showError(
        extractApiErrorMessage(error, t("admin.settings.emailBroadcast.draftClearFailed")),
      );
    }
  }
}

async function loadEmailBroadcastTasks(): Promise<void> {
  emailBroadcastTasksLoading.value = true;
  try {
    const result = await adminAPI.settings.listEmailBroadcasts();
    emailBroadcastTasks.value = result.jobs || [];
    activeBatchId.value = result.active_batch_id || "";
    if (emailBroadcastTasks.value.some((task) => isEmailBroadcastActive(task.status))) {
      scheduleEmailBroadcastStatusRefresh();
    } else {
      clearEmailBroadcastStatusTimer();
    }
  } catch (error: unknown) {
    appStore.showError(
      extractApiErrorMessage(error, t("admin.settings.emailBroadcast.statusFailed")),
    );
  } finally {
    emailBroadcastTasksLoading.value = false;
  }
}

function clearEmailBroadcastStatusTimer(): void {
  if (emailBroadcastStatusTimer != null) {
    window.clearTimeout(emailBroadcastStatusTimer);
    emailBroadcastStatusTimer = null;
  }
}

function scheduleEmailBroadcastStatusRefresh(): void {
  clearEmailBroadcastStatusTimer();
  emailBroadcastStatusTimer = window.setTimeout(async () => {
    await loadEmailBroadcastTasks();
  }, 5000);
}

function isEmailBroadcastActive(status: string): boolean {
  return status === "running" || status === "canceling";
}

function canCancelEmailBroadcast(task: EmailBroadcastStatusResponse): boolean {
  return task.status === "running";
}

function canResumeEmailBroadcast(task: EmailBroadcastStatusResponse): boolean {
  if (isEmailBroadcastActive(task.status)) return false;
  const done = task.sent_count + task.skipped_count;
  return done < task.target_count;
}

function canResendFailedEmailBroadcast(task: EmailBroadcastStatusResponse): boolean {
  return !isEmailBroadcastActive(task.status) && task.failure_count > 0;
}

async function cancelEmailBroadcastTask(batchId: string): Promise<void> {
  emailBroadcastOperatingBatch.value = batchId;
  try {
    await adminAPI.settings.cancelEmailBroadcast(batchId);
    await loadEmailBroadcastTasks();
    appStore.showSuccess(t("admin.settings.emailBroadcast.cancelRequested"));
  } catch (error: unknown) {
    appStore.showError(
      extractApiErrorMessage(error, t("admin.settings.emailBroadcast.operationFailed")),
    );
  } finally {
    emailBroadcastOperatingBatch.value = null;
  }
}

async function resumeEmailBroadcastTask(
  batchId: string,
  mode: NonNullable<ResumeEmailBroadcastRequest["mode"]>,
): Promise<void> {
  emailBroadcastOperatingBatch.value = batchId;
  try {
    const result = await adminAPI.settings.resumeEmailBroadcast(batchId, { mode });
    await loadEmailBroadcastTasks();
    scheduleEmailBroadcastStatusRefresh();
    appStore.showSuccess(
      t("admin.settings.emailBroadcast.resumed", {
        count: result.target_count,
        batch: result.batch_id,
      }),
    );
  } catch (error: unknown) {
    appStore.showError(
      extractApiErrorMessage(error, t("admin.settings.emailBroadcast.operationFailed")),
    );
  } finally {
    emailBroadcastOperatingBatch.value = null;
  }
}

function emailBroadcastProgress(task: EmailBroadcastStatusResponse): number {
  if (!task.target_count) return 0;
  const done = task.sent_count + task.skipped_count + task.failure_count;
  return Math.max(0, Math.min(100, Math.round((done / task.target_count) * 100)));
}

function emailBroadcastStatusLabel(status: string): string {
  const key = `admin.settings.emailBroadcast.statuses.${status}`;
  const label = t(key);
  return label === key ? status : label;
}

function emailBroadcastStatusBadgeClass(status: string): string {
  switch (status) {
    case "completed":
      return "badge-success";
    case "running":
    case "canceling":
      return "badge-info";
    case "interrupted":
      return "badge-warning";
    case "canceled":
      return "badge-gray";
    default:
      return "badge-secondary";
  }
}

function formatEmailBroadcastDate(value?: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

onMounted(() => {
  loadEmailBroadcastTasks();
  loadEmailBroadcastDraft();
});

onUnmounted(() => {
  clearEmailBroadcastStatusTimer();
});
</script>
