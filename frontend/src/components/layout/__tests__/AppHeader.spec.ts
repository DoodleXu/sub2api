import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AppHeader from '@/components/layout/AppHeader.vue'

const { routeState, authState, appState, adminSettingsState, onboardingState } = vi.hoisted(() => ({
  routeState: {
    path: '/admin/operations/daily-checkin',
    name: 'AdminDailyCheckin',
    params: {} as Record<string, unknown>,
    meta: {} as Record<string, unknown>
  },
  authState: {
    user: null as Record<string, unknown> | null,
    isAdmin: false,
    isSimpleMode: true,
    logout: vi.fn()
  },
  appState: {
    contactInfo: '',
    docUrl: '',
    cachedPublicSettings: null as { custom_menu_items?: Array<{ id: string; label: string }> } | null,
    toggleMobileSidebar: vi.fn()
  },
  adminSettingsState: {
    customMenuItems: [] as Array<{ id: string; label: string }>
  },
  onboardingState: {
    replay: vi.fn()
  }
}))

vi.mock('vue-router', () => ({
  useRoute: () => routeState,
  useRouter: () => ({
    push: vi.fn()
  })
}))

vi.mock('@/stores', () => ({
  useAppStore: () => appState,
  useAuthStore: () => authState,
  useOnboardingStore: () => onboardingState
}))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => adminSettingsState
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

function mountHeader() {
  return mount(AppHeader, {
    global: {
      mocks: {
        $t: (key: string) => key
      },
      stubs: {
        AnnouncementBell: { template: '<div data-testid="announcement-bell" />' },
        DailyCheckinButton: { template: '<div data-testid="daily-checkin-button" />' },
        SubscriptionProgressMini: { template: '<div data-testid="subscription-progress-mini" />' },
        LocaleSwitcher: { template: '<div data-testid="locale-switcher" />' },
        Icon: { template: '<span />' },
        RouterLink: { template: '<a><slot /></a>' }
      }
    }
  })
}

describe('AppHeader', () => {
  beforeEach(() => {
    routeState.path = '/admin/operations/daily-checkin'
    routeState.name = 'AdminDailyCheckin'
    routeState.params = {}
    routeState.meta = {}
    authState.user = {
      id: 1,
      username: 'admin',
      email: 'admin@example.com',
      role: 'admin',
      balance: 10
    }
    authState.isAdmin = true
    authState.isSimpleMode = true
    authState.logout.mockReset()
    appState.contactInfo = ''
    appState.docUrl = ''
    appState.cachedPublicSettings = null
    appState.toggleMobileSidebar.mockReset()
    adminSettingsState.customMenuItems = []
    onboardingState.replay.mockReset()
  })

  it('keeps the daily check-in button visible on admin routes', () => {
    const wrapper = mountHeader()

    expect(wrapper.find('[data-testid="daily-checkin-button"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="subscription-progress-mini"]').exists()).toBe(false)
  })

  it('hides the daily check-in button when no user is logged in', () => {
    authState.user = null

    const wrapper = mountHeader()

    expect(wrapper.find('[data-testid="daily-checkin-button"]').exists()).toBe(false)
  })
})
