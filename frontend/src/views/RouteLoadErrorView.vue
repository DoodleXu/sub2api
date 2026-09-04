<template>
  <main
    class="flex min-h-screen items-center justify-center bg-gray-50 px-4 py-12 dark:bg-dark-950"
  >
    <section class="w-full max-w-lg text-center" role="alert">
      <div
        class="mx-auto flex h-14 w-14 items-center justify-center rounded-lg bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300"
      >
        <Icon name="exclamationTriangle" size="xl" />
      </div>
      <h1 class="mt-5 text-2xl font-semibold text-gray-900 dark:text-white">
        {{ t("common.routeLoadErrorTitle") }}
      </h1>
      <p class="mt-2 text-sm leading-6 text-gray-600 dark:text-gray-300">
        {{ t("common.routeLoadErrorDescription") }}
      </p>
      <div class="mt-7 flex flex-col justify-center gap-3 sm:flex-row">
        <button type="button" class="btn btn-primary" @click="retry">
          <Icon name="refresh" size="md" class="mr-2" />
          {{ t("common.reloadPage") }}
        </button>
        <RouterLink to="/dashboard" class="btn btn-secondary">
          <Icon name="home" size="md" class="mr-2" />
          {{ t("nav.dashboard") }}
        </RouterLink>
      </div>
    </section>
  </main>
</template>

<script setup lang="ts">
import { useI18n } from "vue-i18n";
import { useRoute } from "vue-router";

import Icon from "@/components/icons/Icon.vue";
import {
  CHUNK_RELOAD_ATTEMPT_KEY,
  resolveChunkRetryPath,
} from "@/router/chunkLoadRecovery";

const { t } = useI18n();
const route = useRoute();

function retry(): void {
  sessionStorage.removeItem(CHUNK_RELOAD_ATTEMPT_KEY);
  window.location.assign(
    resolveChunkRetryPath(route.query.from, window.location.origin),
  );
}
</script>
