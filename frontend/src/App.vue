<script setup lang="ts">
import { RouterView, useRouter, useRoute } from 'vue-router'
import { computed, defineAsyncComponent, onMounted, onBeforeUnmount, watch } from 'vue'
import Toast from '@/components/common/Toast.vue'
import NavigationProgress from '@/components/common/NavigationProgress.vue'
import {
  applyRouteSEO,
  resolveCustomPageSEO,
  resolveDocumentTitle,
  resolveLegalDocumentSEO,
  resolvePageDescription,
} from '@/router/title'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { getSetupStatus } from '@/api/setup'

const router = useRouter()
const route = useRoute()
const appStore = useAppStore()
const authStore = useAuthStore()
const AnnouncementPopup = defineAsyncComponent(() => import('@/components/common/AnnouncementPopup.vue'))

type SubscriptionStore = Awaited<ReturnType<typeof import('@/stores/subscriptions')['useSubscriptionStore']>>
type AnnouncementStore = Awaited<ReturnType<typeof import('@/stores/announcements')['useAnnouncementStore']>>

let subscriptionStore: SubscriptionStore | null = null
let announcementStore: AnnouncementStore | null = null
let visibilityListenerRegistered = false
let delayedAnnouncementTimer: ReturnType<typeof setTimeout> | null = null
type IdleHandle = number | ReturnType<typeof setTimeout>
let setupStatusCheckHandle: IdleHandle | null = null

const shouldUseUserRuntime = computed(() => (
  authStore.isAuthenticated &&
  !route.path.startsWith('/admin')
))

/**
 * Update favicon dynamically
 * @param logoUrl - URL of the logo to use as favicon
 */
function updateFavicon(logoUrl: string) {
  // Find existing favicon link or create new one
  let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
  if (!link) {
    link = document.createElement('link')
    link.rel = 'icon'
    document.head.appendChild(link)
  }
  link.type = logoUrl.endsWith('.svg') ? 'image/svg+xml' : 'image/x-icon'
  link.href = logoUrl
}

// Watch for site settings changes and update favicon/title
watch(
  () => appStore.siteLogo,
  (newLogo) => {
    if (newLogo) {
      updateFavicon(newLogo)
    }
  },
  { immediate: true }
)

function onVisibilityChange() {
  if (document.visibilityState === 'visible' && shouldUseUserRuntime.value && announcementStore) {
    announcementStore.fetchAnnouncements()
  }
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

  if (!shouldUseUserRuntime.value) {
    return
  }

  subscriptionStore = useSubscriptionStore()
  announcementStore = useAnnouncementStore()

  subscriptionStore.fetchActiveSubscriptions().catch((error) => {
    console.error('Failed to preload subscriptions:', error)
  })
  subscriptionStore.startPolling()

  if (forceAnnouncement) {
    clearDelayedAnnouncementTimer()
    delayedAnnouncementTimer = setTimeout(() => {
      delayedAnnouncementTimer = null
      if (shouldUseUserRuntime.value) {
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
  } else {
    subscriptionStore?.stopPolling()
  }
}

watch(
  [
    () => authStore.isAuthenticated,
    () => route.path,
  ],
  ([isAuthenticated, path], oldValue) => {
    const enabled = isAuthenticated && !path.startsWith('/admin')
    if (!enabled) {
      stopUserRuntime(!isAuthenticated)
      return
    }
    void ensureUserRuntimeData(oldValue?.[0] === false)
  },
  { immediate: true }
)

onBeforeUnmount(() => {
  stopUserRuntime(false)
  cancelSetupStatusCheck()
})

function applyCurrentRouteSEO() {
  const siteName = appStore.siteName || 'Sub2API'
  const siteSubtitle = appStore.cachedPublicSettings?.site_subtitle
  let title = resolveDocumentTitle(route.meta.title, appStore.siteName, route.meta.titleKey as string)
  let description = resolvePageDescription(route.meta.descriptionKey as string | undefined, siteSubtitle)
  let indexable: boolean | undefined

  if (route.name === 'CustomPage') {
    const id = route.params.id as string
    const item = appStore.cachedPublicSettings?.custom_menu_items?.find((menuItem) => menuItem.id === id)
    const seo = resolveCustomPageSEO(item, siteName, siteSubtitle)
    title = seo.title
    description = seo.description
    indexable = seo.indexable
  } else if (route.name === 'LegalDocument') {
    const id = route.params.documentId as string
    const document = appStore.cachedPublicSettings?.login_agreement_documents?.find((doc) => doc.id === id)
    const seo = resolveLegalDocumentSEO(document, siteName, siteSubtitle)
    title = seo.title
    description = seo.description
    indexable = seo.indexable
  }

  applyRouteSEO({
    path: route.path,
    title,
    description,
    siteName,
    image: appStore.siteLogo || '/logo.png',
    indexable,
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
  () => route.fullPath,
  () => {
    if (appStore.publicSettingsLoaded) {
      applyCurrentRouteSEO()
    }
  }
)
</script>

<template>
  <NavigationProgress />
  <RouterView />
  <Toast />
  <AnnouncementPopup v-if="shouldUseUserRuntime" />
</template>
