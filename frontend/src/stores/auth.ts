/**
 * Authentication Store
 * Manages user authentication state, login/logout, token refresh, and token persistence
 */

import { defineStore } from 'pinia'
import { ref, computed, readonly, onScopeDispose } from 'vue'
import { authAPI, isTotp2FARequired, passkeyAPI, type LoginResponse } from '@/api'
import {
  AUTH_USER_KEY,
  clearStoredAuthSessionIfMatches,
  clearPendingOAuthTokenContext,
  getPendingOAuthTokenContext,
  getStoredAuthSnapshot,
  getStoredTokenExpiresAt,
  persistAccessToken,
  persistAuthSession,
  setStoredTokenExpiresAt,
} from '@/utils/authStorage'
import {
  publishAuthSessionEvent,
  subscribeToAuthSessionEvents,
} from '@/utils/authSessionEvents'
import type { ReceivedAuthSessionEvent } from '@/utils/authSessionEvents'
import type {
  User,
  LoginRequest,
  RegisterRequest,
  AuthResponse,
  ActionCaptchaRequestProof
} from '@/types'

const PENDING_AUTH_SESSION_KEY = 'pending_auth_session'
const AUTO_REFRESH_INTERVAL = 60 * 1000 // 60 seconds for user data refresh
const TOKEN_REFRESH_BUFFER = 120 * 1000 // 120 seconds before expiry to refresh token

type PendingAuthTokenField = 'pending_auth_token' | 'pending_oauth_token'

interface PendingAuthSessionSummary {
  token: string
  token_field: PendingAuthTokenField
  provider: string
  redirect?: string
  adoption_required?: boolean
  suggested_display_name?: string
  suggested_avatar_url?: string
}

function normalizePendingAuthTokenField(value: unknown): PendingAuthTokenField {
  return value === 'pending_oauth_token' ? 'pending_oauth_token' : 'pending_auth_token'
}

function getPersistedPendingAuthSession(): PendingAuthSessionSummary | null {
  const raw = localStorage.getItem(PENDING_AUTH_SESSION_KEY)
  if (!raw) {
    return null
  }

  try {
    const parsed = JSON.parse(raw) as Partial<PendingAuthSessionSummary> | null
    const provider = typeof parsed?.provider === 'string' ? parsed.provider.trim() : ''
    if (!provider) {
      localStorage.removeItem(PENDING_AUTH_SESSION_KEY)
      return null
    }
    return {
      token: typeof parsed?.token === 'string' ? parsed.token : '',
      token_field: normalizePendingAuthTokenField(parsed?.token_field),
      provider,
      redirect: typeof parsed?.redirect === 'string' ? parsed.redirect : undefined,
      adoption_required: typeof parsed?.adoption_required === 'boolean' ? parsed.adoption_required : undefined,
      suggested_display_name: typeof parsed?.suggested_display_name === 'string' ? parsed.suggested_display_name : undefined,
      suggested_avatar_url: typeof parsed?.suggested_avatar_url === 'string' ? parsed.suggested_avatar_url : undefined
    }
  } catch {
    localStorage.removeItem(PENDING_AUTH_SESSION_KEY)
    return null
  }
}

function persistPendingAuthSession(session: PendingAuthSessionSummary): void {
  localStorage.setItem(PENDING_AUTH_SESSION_KEY, JSON.stringify(session))
}

function clearPendingAuthSessionStorage(): void {
  localStorage.removeItem(PENDING_AUTH_SESSION_KEY)
}

export const useAuthStore = defineStore('auth', () => {
  // ==================== State ====================

  const user = ref<User | null>(null)
  const token = ref<string | null>(null)
  const refreshTokenValue = ref<string | null>(null)
  const tokenExpiresAt = ref<number | null>(null) // 过期时间戳（毫秒）
  const identityVerified = ref(false)
  const runMode = ref<'standard' | 'simple'>('standard')
  const pendingAuthSession = ref<PendingAuthSessionSummary | null>(null)
  let refreshIntervalId: ReturnType<typeof setInterval> | null = null
  let tokenRefreshTimeoutId: ReturnType<typeof setTimeout> | null = null
  let authCheckPromise: Promise<void> | null = null
  let identityValidationPromise: Promise<boolean> | null = null

  function resetInMemoryAuth(): void {
    stopAutoRefresh()
    stopTokenRefresh()
    token.value = null
    refreshTokenValue.value = null
    tokenExpiresAt.value = null
    identityVerified.value = false
    user.value = null
    runMode.value = 'standard'
    pendingAuthSession.value = null
  }

  function ownsPersistedSession(): boolean {
    const stored = getStoredAuthSnapshot()
    if (!token.value || stored.accessToken !== token.value) return false
    if (refreshTokenValue.value && stored.refreshToken !== refreshTokenValue.value) {
      // OAuth callbacks keep the refresh token in sessionStorage until /auth/me
      // confirms the new identity. Allow that short-lived staging state, but
      // only while the access token is already the one being validated.
      const pendingOAuth = getPendingOAuthTokenContext()
      if (pendingOAuth.refreshToken !== refreshTokenValue.value) return false
    }
    if (user.value?.id && stored.userId && Number(user.value.id) !== stored.userId) return false
    return true
  }

  function handleCrossTabSessionEvent(event: ReceivedAuthSessionEvent): void {
    const currentUserID = user.value?.id ? Number(user.value.id) : null

    if (
      event.source === 'cross-tab' &&
      (event.type === 'authenticated' || event.type === 'identity_changed') &&
      currentUserID !== null &&
      currentUserID !== event.userId
    ) {
      resetInMemoryAuth()
      return
    }

    if (
      (event.type === 'invalidated' || event.type === 'logged_out') &&
      (event.userId === null || currentUserID === null || currentUserID === event.userId)
    ) {
      resetInMemoryAuth()
    }
  }

  const stopAuthSessionSync = subscribeToAuthSessionEvents(handleCrossTabSessionEvent)
  onScopeDispose(stopAuthSessionSync)

  // ==================== Computed ====================

  const isAuthenticated = computed(() => {
    return !!token.value && !!user.value
  })

  const isAdmin = computed(() => {
    return user.value?.role === 'admin'
  })

  const isSimpleMode = computed(() => runMode.value === 'simple')
  const hasPendingAuthSession = computed(() => pendingAuthSession.value !== null)

  // ==================== Actions ====================

  /**
   * Initialize auth state from localStorage
   * Call this on app startup to restore session
   * Also starts auto-refresh and immediately fetches latest user data
   */
  async function checkAuth(): Promise<void> {
    if (authCheckPromise) return authCheckPromise

    authCheckPromise = (async () => {
      const saved = getStoredAuthSnapshot()
      pendingAuthSession.value = getPersistedPendingAuthSession()

      if (!saved.accessToken || !saved.userRaw) {
        return
      }

      let savedUser: User
      try {
        savedUser = JSON.parse(saved.userRaw) as User
      } catch (error) {
        console.error('Failed to parse persisted authenticated user:', error)
        const cleared = clearStoredAuthSessionIfMatches({
          accessToken: saved.accessToken,
          refreshToken: saved.refreshToken,
          userRaw: saved.userRaw,
        })
        resetInMemoryAuth()
        if (cleared) {
          publishAuthSessionEvent('invalidated', saved.userId, 'invalid_persisted_user')
        }
        return
      }

      try {
        token.value = saved.accessToken
        user.value = savedUser
        refreshTokenValue.value = saved.refreshToken
        tokenExpiresAt.value = getStoredTokenExpiresAt()
        identityVerified.value = false

        const verified = await ensureCurrentUser({ force: true })
        if (!verified) return
        startAutoRefresh()

        if (saved.refreshToken && tokenExpiresAt.value !== null) {
          scheduleTokenRefreshAt(tokenExpiresAt.value)
        }
      } catch (error) {
        console.error('Failed to restore authenticated session:', error)
      }
    })()

    try {
      await authCheckPromise
    } finally {
      authCheckPromise = null
    }
  }

  /**
   * Start auto-refresh interval for user data
   * Refreshes user data every 60 seconds
   */
  function startAutoRefresh(): void {
    // Clear existing interval if any
    stopAutoRefresh()

    refreshIntervalId = setInterval(() => {
      if (token.value) {
        refreshUser().catch((error) => {
          console.error('Auto-refresh user failed:', error)
        })
      }
    }, AUTO_REFRESH_INTERVAL)
  }

  /**
   * Stop auto-refresh interval
   */
  function stopAutoRefresh(): void {
    if (refreshIntervalId) {
      clearInterval(refreshIntervalId)
      refreshIntervalId = null
    }
  }

  /**
   * Schedule proactive token refresh before expiry (based on expiry timestamp)
   * @param expiresAtMs - Token expiry timestamp in milliseconds
   */
  function scheduleTokenRefreshAt(expiresAtMs: number): void {
    // Clear any existing timeout
    if (tokenRefreshTimeoutId) {
      clearTimeout(tokenRefreshTimeoutId)
      tokenRefreshTimeoutId = null
    }

    // Calculate remaining time until refresh (buffer time before expiry)
    const now = Date.now()
    const refreshInMs = Math.max(0, expiresAtMs - now - TOKEN_REFRESH_BUFFER)

    if (refreshInMs <= 0) {
      // Token is about to expire or already expired, refresh immediately
      performTokenRefresh()
      return
    }

    tokenRefreshTimeoutId = setTimeout(() => {
      performTokenRefresh()
    }, refreshInMs)
  }

  /**
   * Schedule proactive token refresh before expiry (based on expires_in seconds)
   * @param expiresInSeconds - Token expiry time in seconds from now
   */
  function scheduleTokenRefresh(expiresInSeconds: number): void {
    const expiresAtMs = Date.now() + expiresInSeconds * 1000
    tokenExpiresAt.value = expiresAtMs
    setStoredTokenExpiresAt(expiresInSeconds)
    scheduleTokenRefreshAt(expiresAtMs)
  }

  /**
   * Perform the actual token refresh
   */
  async function performTokenRefresh(): Promise<void> {
    if (!refreshTokenValue.value || !ownsPersistedSession()) {
      resetInMemoryAuth()
      return
    }

    try {
      const response = await authAPI.refreshToken()

      // Update state
      token.value = response.access_token
      refreshTokenValue.value = response.refresh_token

      // Schedule next refresh (this also updates tokenExpiresAt and localStorage)
      scheduleTokenRefresh(response.expires_in)
    } catch (error) {
      console.error('Token refresh failed:', error)
      // Don't clear auth here - the interceptor will handle 401 errors
    }
  }

  /**
   * Stop token refresh timeout
   */
  function stopTokenRefresh(): void {
    if (tokenRefreshTimeoutId) {
      clearTimeout(tokenRefreshTimeoutId)
      tokenRefreshTimeoutId = null
    }
  }

  /**
   * User login
   * @param credentials - Login credentials (email and password)
   * @returns Promise resolving to the login response (may require 2FA)
   * @throws Error if login fails
   */
  async function login(credentials: LoginRequest): Promise<LoginResponse> {
    const response = await authAPI.login(credentials)

    // If 2FA is required, return the response without setting auth state
    if (isTotp2FARequired(response)) {
      return response
    }

    // Set auth state from the response
    setAuthFromResponse(response)

    return response
  }

  /**
   * Complete login with 2FA code
   * @param tempToken - Temporary token from initial login
   * @param totpCode - 6-digit TOTP code
   * @returns Promise resolving to the authenticated user
   * @throws Error if 2FA verification fails
   */
  async function login2FA(tempToken: string, totpCode: string): Promise<User> {
    const response = await authAPI.login2FA({ temp_token: tempToken, totp_code: totpCode })
    setAuthFromResponse(response)
    return user.value!
  }

  async function loginWithPasskey(proof?: ActionCaptchaRequestProof): Promise<User> {
    const response = await passkeyAPI.login(proof)
    setAuthFromResponse(response)
    return user.value!
  }

  /**
   * Set auth state from an AuthResponse
   * Internal helper function
   */
  function setAuthFromResponse(response: AuthResponse): void {
    const previousUserID = user.value?.id ? Number(user.value.id) : getStoredAuthSnapshot().userId

    stopAutoRefresh()
    stopTokenRefresh()
    token.value = response.access_token

    // Extract run_mode if present
    if (response.user.run_mode) {
      runMode.value = response.user.run_mode
    }
    const { run_mode: _run_mode, ...userData } = response.user
    user.value = userData
    refreshTokenValue.value = response.refresh_token ?? null
    identityVerified.value = true

    persistAuthSession({
      accessToken: response.access_token,
      refreshToken: response.refresh_token ?? null,
      expiresIn: response.expires_in ?? null,
      user: userData,
    })
    clearPendingAuthSession()

    // Start auto-refresh interval for user data
    startAutoRefresh()

    // Start proactive token refresh if we have refresh token and expiry info
    // scheduleTokenRefresh will also store the expiry timestamp
    if (response.refresh_token && response.expires_in) {
      scheduleTokenRefresh(response.expires_in)
    }

    publishAuthSessionEvent(
      previousUserID !== null && previousUserID !== Number(userData.id)
        ? 'identity_changed'
        : 'authenticated',
      Number(userData.id),
      previousUserID !== null && previousUserID !== Number(userData.id)
        ? 'login_identity_changed'
        : undefined,
    )
  }

  /**
   * User registration
   * @param userData - Registration data (username, email, password)
   * @returns Promise resolving to the newly registered and authenticated user
   * @throws Error if registration fails
   */
  async function register(userData: RegisterRequest): Promise<User> {
    const response = await authAPI.register(userData)

    // Use the common helper to set auth state
    setAuthFromResponse(response)

    return user.value!
  }

  /**
   * 直接设置 token（用于 OAuth/SSO 回调），并加载当前用户信息。
   * 会自动读取当前回调标签页中暂存的 refresh_token 和过期时间
   * @param newToken - 后端签发的 JWT access token
   */
  async function setToken(newToken: string): Promise<User> {
    const previousUserID = user.value?.id ? Number(user.value.id) : getStoredAuthSnapshot().userId
    const pendingOAuth = getPendingOAuthTokenContext()
    const hadPendingAuthSession = pendingAuthSession.value !== null

    // Clear any previous in-memory state first. OAuth callback credentials are
    // staged separately in this tab until the identity check succeeds.
    stopAutoRefresh()
    stopTokenRefresh()
    resetInMemoryAuth()

    token.value = newToken
    persistAccessToken(newToken)
    localStorage.removeItem(AUTH_USER_KEY)

    // Read the callback's tab-local token context. It is committed to the
    // shared session only after the server confirms the current identity.
    const savedRefreshToken = pendingOAuth.refreshToken
    const savedExpiresAt = pendingOAuth.tokenExpiresAt

    if (savedRefreshToken) {
      refreshTokenValue.value = savedRefreshToken
    }
    if (savedExpiresAt) {
      tokenExpiresAt.value = savedExpiresAt
    }

    try {
      const userData = await refreshUser()
      startAutoRefresh()

      persistAuthSession({
        accessToken: newToken,
        refreshToken: savedRefreshToken,
        expiresAt: savedExpiresAt,
        user: userData,
      })

      // Start proactive token refresh if we have refresh token and expiry info
      // Note: use !== null to handle case when tokenExpiresAt.value is 0 (expired)
      if (savedRefreshToken && tokenExpiresAt.value !== null) {
        scheduleTokenRefreshAt(tokenExpiresAt.value)
      }

      clearPendingAuthSession()
      clearPendingOAuthTokenContext()
      publishAuthSessionEvent(
        previousUserID !== null && previousUserID !== Number(userData.id)
          ? 'identity_changed'
          : 'authenticated',
        Number(userData.id),
        previousUserID !== null && previousUserID !== Number(userData.id)
          ? 'oauth_identity_changed'
          : undefined,
      )
      return userData
    } catch (error) {
      const cleared = clearStoredAuthSessionIfMatches({ accessToken: newToken })
      clearPendingOAuthTokenContext()
      if (cleared) {
        publishAuthSessionEvent('invalidated', null, 'oauth_identity_validation_failed')
      }
      clearAuth({ preservePendingAuthSession: hadPendingAuthSession })
      throw error
    }
  }

  function setPendingAuthSession(session: PendingAuthSessionSummary | null): void {
    pendingAuthSession.value = session

    if (session) {
      persistPendingAuthSession(session)
      return
    }

    clearPendingAuthSessionStorage()
  }

  function clearPendingAuthSession(): void {
    setPendingAuthSession(null)
  }

  /**
   * User logout
   * Clears all authentication state and persisted data
   */
  async function logout(): Promise<void> {
    try {
      // Call API logout (revokes refresh token on server)
      await authAPI.logout(refreshTokenValue.value)
    } catch (err) {
      // 服务端吊销失败（网络/5xx/超时）不应阻止本地登出，否则用户点了退出仍处于登录态。
      console.warn('Logout API call failed, clearing local session anyway', err)
    } finally {
      // Always clear local state (tokens, user data, refresh timers)
      clearAuth()
    }
  }

  /**
   * Refresh current user data
   * Fetches latest user info from the server
   * @returns Promise resolving to the updated user
   * @throws Error if not authenticated or request fails
   */
  async function refreshUser(): Promise<User> {
    if (!token.value) {
      throw new Error('Not authenticated')
    }

    if (!ownsPersistedSession()) {
      resetInMemoryAuth()
      throw { status: 409, code: 'AUTH_SESSION_CHANGED', message: 'Authentication session changed.' }
    }

    const expectedUserID = user.value?.id ? Number(user.value.id) : null

    try {
      const response = await authAPI.getCurrentUser()
      const returnedUserID = Number(response.data.id)
      if (expectedUserID !== null && returnedUserID !== expectedUserID) {
        const cleared = clearStoredAuthSessionIfMatches({
          accessToken: token.value,
          refreshToken: refreshTokenValue.value,
          userId: expectedUserID,
        })
        resetInMemoryAuth()
        if (cleared) {
          publishAuthSessionEvent('invalidated', expectedUserID, 'auth_identity_mismatch')
        }
        throw { status: 409, code: 'AUTH_IDENTITY_MISMATCH', message: 'Authenticated identity changed.' }
      }
      if (!ownsPersistedSession()) {
        resetInMemoryAuth()
        throw { status: 409, code: 'AUTH_SESSION_CHANGED', message: 'Authentication session changed.' }
      }
      if (response.data.run_mode) {
        runMode.value = response.data.run_mode
      }
      const { run_mode: _run_mode, ...userData } = response.data
      user.value = userData
      identityVerified.value = true

      // Update localStorage
      localStorage.setItem(AUTH_USER_KEY, JSON.stringify(userData))

      return userData
    } catch (error) {
      // If refresh fails with 401, clear auth state
      if ((error as { status?: number }).status === 401) {
        clearAuth({
          preservePendingAuthSession: pendingAuthSession.value !== null,
          eventType: 'invalidated',
          reason: 'current_user_unauthorized',
        })
      }
      throw error
    }
  }

  async function ensureCurrentUser(options: { force?: boolean } = {}): Promise<boolean> {
    if (!token.value) return false
    if (!options.force && identityVerified.value) return true
    if (identityValidationPromise) return identityValidationPromise

    identityValidationPromise = (async () => {
      try {
        await refreshUser()
        return identityVerified.value
      } catch {
        return false
      } finally {
        identityValidationPromise = null
      }
    })()

    return identityValidationPromise
  }

  function reconcilePersistedSession(): void {
    if (!token.value) return
    const stored = getStoredAuthSnapshot()
    if (
      stored.accessToken !== token.value ||
      user.value?.id && stored.userId !== Number(user.value.id)
    ) {
      resetInMemoryAuth()
    }
  }

  /**
   * Clear all authentication state
   * Internal helper function
   */
  function clearAuth(options?: {
    preservePendingAuthSession?: boolean
    eventType?: 'logged_out' | 'invalidated'
    reason?: string
  }): void {
    const previousUserID = user.value?.id ? Number(user.value.id) : null
    const previousAccessToken = token.value
    const previousRefreshToken = refreshTokenValue.value
    const clearedPersistedSession = previousAccessToken
      ? clearStoredAuthSessionIfMatches({
          accessToken: previousAccessToken,
          refreshToken: previousRefreshToken,
          userId: previousUserID,
        })
      : false

    resetInMemoryAuth()

    if (clearedPersistedSession) {
      publishAuthSessionEvent(
        options?.eventType ?? 'logged_out',
        previousUserID,
        options?.reason,
      )
    }

    if (options?.preservePendingAuthSession) {
      pendingAuthSession.value = getPersistedPendingAuthSession()
      return
    }

    pendingAuthSession.value = null
    clearPendingAuthSessionStorage()
  }

  // ==================== Return Store API ====================

  return {
    // State
    user,
    token,
    runMode: readonly(runMode),
    pendingAuthSession: readonly(pendingAuthSession),

    // Computed
    isAuthenticated,
    isAdmin,
    identityVerified: readonly(identityVerified),
    isSimpleMode,
    hasPendingAuthSession,

    // Actions
    login,
    loginWithPasskey,
    login2FA,
    register,
    setToken,
    logout,
    checkAuth,
    ensureCurrentUser,
    reconcilePersistedSession,
    refreshUser,
    setPendingAuthSession,
    clearPendingAuthSession
  }
})
