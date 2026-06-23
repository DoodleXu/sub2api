<script setup lang="ts">
import { RouterView, useRouter, useRoute } from 'vue-router'
import { computed, defineAsyncComponent, onMounted, onBeforeUnmount, watch } from 'vue'
import Toast from '@/components/common/Toast.vue'
import NavigationProgress from '@/components/common/NavigationProgress.vue'
import AdminComplianceDialog from '@/components/admin/AdminComplianceDialog.vue'
import {
  applyRouteSEO,
  resolveRouteSEO,
} from '@/router/title'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { useAdminComplianceStore } from '@/stores/adminCompliance'
import { useAdminSettingsStore } from '@/stores/adminSettings'
import { getSetupStatus } from '@/api/setup'
import { updateFavicon } from '@/utils/favicon'

const router = useRouter()
const route = useRoute()
const appStore = useAppStore()
const authStore = useAuthStore()
const adminComplianceStore = useAdminComplianceStore()
const AnnouncementPopup = defineAsyncComponent(() => import('@/components/common/AnnouncementPopup.vue'))
const adminSettingsStore = useAdminSettingsStore()

type SubscriptionStore = Awaited<ReturnType<typeof import('@/stores/subscriptions')['useSubscriptionStore']>>
type AnnouncementStore = Awaited<ReturnType<typeof import('@/stores/announcements')['useAnnouncementStore']>>

let subscriptionStore: SubscriptionStore | null = null
let announcementStore: AnnouncementStore | null = null
let visibilityListenerRegistered = false
let delayedAnnouncementTimer: ReturnType<typeof setTimeout> | null = null
type IdleHandle = number | ReturnType<typeof setTimeout>
let setupStatusCheckHandle: IdleHandle | null = null

const shouldUseSubscriptionRuntime = computed(() => (
  authStore.isAuthenticated &&
  !route.path.startsWith('/admin')
))

const shouldUseAnnouncementRuntime = computed(() => authStore.isAuthenticated)

// Watch for site settings changes and update favicon/title
watch(
  () => appStore.siteLogo,
  (newLogo) => {
    updateFavicon(newLogo || '/logo.png')
  },
  { immediate: true }
)

function onVisibilityChange() {
  if (document.visibilityState === 'visible' && shouldUseAnnouncementRuntime.value && announcementStore) {
    announcementStore.fetchAnnouncements()
  }
}

function onAdminComplianceRequired(event: Event) {
  const detail = (event as CustomEvent<Record<string, string>>).detail || {}
  adminComplianceStore.requireAcknowledgement(detail)
}

function registerVisibilityListener() {
  if (visibilityListenerRegistered) return
  document.addEventListener('visibilitychange', onVisibilityChange)
  visibilityListenerRegistered = true
}

function unregisterVisibilityListener() {
  if (!visibilityListenerRegistered) return
  document.removeEventListener('visibilitychange', onVisibilityChange)
  visibilityListenerRegistered = false
}

function clearDelayedAnnouncementTimer() {
  if (delayedAnnouncementTimer !== null) {
    clearTimeout(delayedAnnouncementTimer)
    delayedAnnouncementTimer = null
  }
}

async function ensureUserRuntimeData(forceAnnouncement = false) {
  const [
    { useSubscriptionStore },
    { useAnnouncementStore },
  ] = await Promise.all([
    import('@/stores/subscriptions'),
    import('@/stores/announcements'),
  ])

  if (!shouldUseSubscriptionRuntime.value && !shouldUseAnnouncementRuntime.value) {
    return
  }

  if (shouldUseSubscriptionRuntime.value) {
    subscriptionStore = useSubscriptionStore()
    subscriptionStore.fetchActiveSubscriptions().catch((error) => {
      console.error('Failed to preload subscriptions:', error)
    })
    subscriptionStore.startPolling()
  } else {
    subscriptionStore?.stopPolling()
  }

  if (!shouldUseAnnouncementRuntime.value) {
    return
  }

  announcementStore = useAnnouncementStore()

  if (forceAnnouncement) {
    clearDelayedAnnouncementTimer()
    delayedAnnouncementTimer = setTimeout(() => {
      delayedAnnouncementTimer = null
      if (shouldUseAnnouncementRuntime.value) {
        announcementStore?.fetchAnnouncements(true)
      }
    }, 3000)
  } else {
    announcementStore.fetchAnnouncements()
  }

  registerVisibilityListener()
}

function stopUserRuntime(clearState: boolean) {
  clearDelayedAnnouncementTimer()
  unregisterVisibilityListener()
  if (clearState) {
    subscriptionStore?.clear()
    announcementStore?.reset()
    adminComplianceStore.reset()
  } else {
    subscriptionStore?.stopPolling()
  }
}

watch(
  [
    () => authStore.isAuthenticated,
    () => route.path,
  ],
  ([isAuthenticated], oldValue) => {
    if (!isAuthenticated) {
      stopUserRuntime(true)
      return
    }
    if (authStore.isAdmin) {
      adminComplianceStore.fetchStatus().catch((error) => {
        console.error('Failed to fetch admin compliance status:', error)
      })
    }
    void ensureUserRuntimeData(oldValue?.[0] === false)
  },
  { immediate: true }
)

onBeforeUnmount(() => {
  stopUserRuntime(false)
  cancelSetupStatusCheck()
  window.removeEventListener('admin-compliance-required', onAdminComplianceRequired)
})

function applyCurrentRouteSEO() {
  const siteName = appStore.siteName || 'Sub2API'
  const siteSubtitle = appStore.cachedPublicSettings?.site_subtitle
  const customMenuItems = [
    ...(appStore.cachedPublicSettings?.custom_menu_items ?? []),
    ...(authStore.isAdmin ? adminSettingsStore.customMenuItems : []),
  ]
  const seo = resolveRouteSEO(route, {
    siteName,
    siteSubtitle,
    customMenuItems,
    loginAgreementDocuments: appStore.cachedPublicSettings?.login_agreement_documents ?? [],
  })

  applyRouteSEO({
    path: route.path,
    title: seo.title,
    description: seo.description,
    siteName,
    image: appStore.siteLogo || '/logo.png',
    indexable: seo.indexable,
  })
}

function runSetupStatusCheck() {
  setupStatusCheckHandle = null
  if (route.path === '/setup') {
    return
  }

  getSetupStatus()
    .then((status) => {
      if (status.needs_setup && route.path !== '/setup') {
        router.replace('/setup')
      }
    })
    .catch(() => {
      // If setup endpoint fails, assume normal mode and continue
    })
}

function cancelSetupStatusCheck() {
  if (setupStatusCheckHandle === null) {
    return
  }
  if (typeof window.cancelIdleCallback === 'function' && typeof setupStatusCheckHandle === 'number') {
    window.cancelIdleCallback(setupStatusCheckHandle)
  } else {
    clearTimeout(setupStatusCheckHandle)
  }
  setupStatusCheckHandle = null
}

function scheduleSetupStatusCheck() {
  if (route.path === '/setup' || setupStatusCheckHandle !== null) {
    return
  }

  if (!authStore.isAuthenticated) {
    runSetupStatusCheck()
    return
  }

  if (typeof window.requestIdleCallback === 'function') {
    setupStatusCheckHandle = window.requestIdleCallback(runSetupStatusCheck, { timeout: 2500 })
  } else {
    setupStatusCheckHandle = window.setTimeout(runSetupStatusCheck, 1200)
  }
}

onMounted(() => {
  window.addEventListener('admin-compliance-required', onAdminComplianceRequired)

  // Keep the first-run guard, but defer it for authenticated dashboards so it
  // does not compete with first-screen data.
  scheduleSetupStatusCheck()

  // Load public settings into appStore (will be cached for other components).
  void appStore.fetchPublicSettings().then(() => {
    // Re-resolve SEO metadata now that site settings are available.
    applyCurrentRouteSEO()
  })
})

watch(
  [
    () => route.fullPath,
    () => route.meta.title,
    () => route.meta.titleKey,
    () => route.meta.descriptionKey,
    () => appStore.siteName,
    () => appStore.siteLogo,
    () => appStore.cachedPublicSettings?.site_subtitle,
    () => appStore.cachedPublicSettings?.custom_menu_items,
    () => appStore.cachedPublicSettings?.login_agreement_documents,
    () => authStore.isAdmin,
    () => adminSettingsStore.customMenuItems,
  ],
  () => {
    if (appStore.publicSettingsLoaded) {
      applyCurrentRouteSEO()
    }
  },
  { deep: true }
)
</script>

<template>
  <NavigationProgress />
  <RouterView />
  <Toast />
  <AnnouncementPopup v-if="shouldUseAnnouncementRuntime" />
  <AdminComplianceDialog />
</template>
