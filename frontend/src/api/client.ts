/**
 * Axios HTTP Client Configuration
 * Base client with interceptors for authentication, token refresh, and error handling
 */

import axios, { AxiosInstance, AxiosError, InternalAxiosRequestConfig, AxiosResponse } from 'axios'
import type { ApiResponse } from '@/types'
import { getLocale } from '@/i18n'
import {
  ADMIN_UI_REQUEST_HEADER,
  USER_UI_REQUEST_HEADER,
  shouldMarkAdminUIRequest,
  shouldMarkUserUIRequest,
} from './adminUIRequest'
import { refreshAuthTokens } from './tokenRefresh'
import { getAPIBaseURL } from './url'
import {
  clearStoredAuthSessionIfMatches,
  getStoredAuthSnapshot,
  getStoredAuthToken,
} from '@/utils/authStorage'
import { publishAuthSessionEvent } from '@/utils/authSessionEvents'
export { buildApiUrl, buildGatewayUrl } from './url'

export function isAPIErrorStatus(error: unknown, expectedStatus: number): boolean {
	if (typeof error !== 'object' || error === null) return false
	return (error as { status?: unknown }).status === expectedStatus
}

// ==================== Axios Instance Configuration ====================

export const apiClient: AxiosInstance = axios.create({
  baseURL: getAPIBaseURL(),
  withCredentials: true,
  timeout: 30000,
  headers: {
    'Content-Type': 'application/json'
  }
})

export function isOpsMonitoringPath(pathname: string): boolean {
  return pathname === '/admin/ops' || pathname.startsWith('/admin/ops/')
}

function isAuthBootstrapRequest(url: string): boolean {
  const path = url.split('?')[0]
  return /\/auth\/(login|login\/2fa|register)$/.test(path)
}

function isAuthRefreshRequest(url: string): boolean {
  return /\/auth\/refresh$/.test(url.split('?')[0])
}

function isAdminAPIRequest(url: string): boolean {
  const path = url.split('?')[0]
  return path.includes('/admin/') || path.endsWith('/admin')
}

async function decodeBlobAPIError(data: unknown, contentType: unknown): Promise<unknown> {
  if (typeof Blob === 'undefined' || !(data instanceof Blob)) return data
  const mime = `${data.type || ''};${String(contentType || '')}`.toLowerCase()
  if (!mime.includes('json')) return data
  try {
    const text = typeof data.text === 'function'
      ? await data.text()
      : await new Promise<string>((resolve, reject) => {
          const reader = new FileReader()
          reader.onload = () => resolve(String(reader.result || ''))
          reader.onerror = () => reject(reader.error)
          reader.readAsText(data)
        })
    return JSON.parse(text)
  } catch {
    return data
  }
}
// ==================== Request Interceptor ====================

// Get user's timezone
const getUserTimezone = (): string => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone
  } catch {
    return 'UTC'
  }
}

apiClient.interceptors.request.use(
  (config: InternalAxiosRequestConfig) => {
    // Public login/register requests must not inherit the previous account's
    // Authorization header. Other requests use the committed shared session.
    const token = getStoredAuthToken()
    const requestURL = String(config.url || '')
    if (config.headers) {
      if (isAuthBootstrapRequest(requestURL)) {
        delete config.headers.Authorization
        delete config.headers.authorization
      } else if (token) {
        config.headers.Authorization = `Bearer ${token}`
      }
      // API responses are user- and role-specific. This request hint helps
      // proxies that ignore the origin response policy during revalidation.
      config.headers['Cache-Control'] = 'no-cache'
    }

    // Attach locale for backend translations
    if (config.headers) {
      config.headers['Accept-Language'] = getLocale()
    }

    // Attach timezone for all GET requests (backend may use it for default date ranges)
    if (config.method === 'get') {
      if (!config.params) {
        config.params = {}
      }
      config.params.timezone = getUserTimezone()
    }

    if (config.headers) {
      const requestURL = String(config.url || '')
      if (shouldMarkAdminUIRequest(requestURL)) {
        config.headers[ADMIN_UI_REQUEST_HEADER] = '1'
      }
      if (shouldMarkUserUIRequest(requestURL)) {
        config.headers[USER_UI_REQUEST_HEADER] = '1'
      }
    }

    return config
  },
  (error) => {
    return Promise.reject(error)
  }
)

// ==================== Response Interceptor ====================

apiClient.interceptors.response.use(
  (response: AxiosResponse) => {
    // Unwrap standard API response format { code, message, data }
    const apiResponse = response.data as ApiResponse<unknown>
    if (apiResponse && typeof apiResponse === 'object' && 'code' in apiResponse) {
      if (apiResponse.code === 0) {
        // Success - return the data portion
        response.data = apiResponse.data
      } else {
        // API error
        const resp = apiResponse as unknown as Record<string, unknown>
        return Promise.reject({
          status: response.status,
          code: apiResponse.code,
          message: apiResponse.message || 'Unknown error',
          reason: resp.reason,
          metadata: resp.metadata,
        })
      }
    }
    return response
  },
  async (error: AxiosError<ApiResponse<unknown>>) => {
    // Request cancellation: keep the original axios cancellation error so callers can ignore it.
    // Otherwise we'd misclassify it as a generic "network error".
    if (error.code === 'ERR_CANCELED' || axios.isCancel(error)) {
      return Promise.reject(error)
    }

    const originalRequest = error.config as InternalAxiosRequestConfig & { _retry?: boolean }

    // Handle common errors
    if (error.response) {
      const { status } = error.response
      const contentType = error.response.headers?.['content-type']
      const data = await decodeBlobAPIError(error.response.data, contentType)
      const url = String(error.config?.url || '')

      // Validate `data` shape to avoid HTML error pages breaking our error handling.
      const apiData = (typeof data === 'object' && data !== null ? data : {}) as Record<string, any>
      const isAuthBootstrap = isAuthBootstrapRequest(url)
      const isAuthRefresh = isAuthRefreshRequest(url)

      if (status === 403 && isAdminAPIRequest(url)) {
        publishAuthSessionEvent(
          'role_mismatch',
          getStoredAuthSnapshot().userId,
          'admin_api_forbidden',
        )
      }

      // Ops monitoring disabled: treat as feature-flagged 404, and proactively redirect away
      // from ops pages to avoid broken UI states.
      if (status === 404 && apiData.message === 'Ops monitoring is disabled') {
        try {
          localStorage.setItem('ops_monitoring_enabled_cached', 'false')
        } catch {
          // ignore localStorage failures
        }
        try {
          window.dispatchEvent(new CustomEvent('ops-monitoring-disabled'))
        } catch {
          // ignore event failures
        }

        if (isOpsMonitoringPath(window.location.pathname)) {
          window.location.href = '/admin/settings'
        }

        return Promise.reject({
          status,
          code: 'OPS_DISABLED',
          message: apiData.message || error.message,
          url
        })
      }

      if (status === 423 && apiData.code === 'ADMIN_COMPLIANCE_ACK_REQUIRED') {
        try {
          window.dispatchEvent(new CustomEvent('admin-compliance-required', {
            detail: apiData.metadata || {}
          }))
        } catch {
          // ignore event failures
        }

        return Promise.reject({
          status,
          code: apiData.code,
          message: apiData.message || error.message,
          metadata: apiData.metadata,
        })
      }

      // 401: Try to refresh the token if we have a refresh token
      // This handles TOKEN_EXPIRED, INVALID_TOKEN, TOKEN_REVOKED, etc.
      if (status === 401 && !originalRequest._retry) {
        const refreshToken = getStoredAuthSnapshot().refreshToken

        // A failed login/register attempt must not log out an already active
        // account in the same browser tab.
        if (isAuthBootstrap) {
          return Promise.reject({
            status,
            code: apiData.code,
            reason: apiData.reason,
            error: apiData.error,
            message: apiData.message || apiData.detail || error.message,
            metadata: apiData.metadata,
          })
        }

        // If we have a refresh token and this is not an auth endpoint, try to refresh
        if (refreshToken && !isAuthRefresh) {
          const refreshSessionUser = getStoredAuthSnapshot().userRaw
          originalRequest._retry = true
          const headers = originalRequest.headers as Record<string, unknown> | undefined
          const authHeader = headers?.Authorization ?? headers?.authorization
          const failedAccessToken =
            typeof authHeader === 'string' && authHeader.startsWith('Bearer ')
              ? authHeader.slice('Bearer '.length)
              : null

          try {
            const tokens = await refreshAuthTokens({ failedAccessToken })

            // Retry the original request with the refreshed token
            if (originalRequest.headers) {
              originalRequest.headers.Authorization = `Bearer ${tokens.access_token}`
            }
            return apiClient(originalRequest)
          } catch {
            // A stale request must never destroy a session that was logged out or replaced while
            // its refresh was in flight (for example, when another tab signs in as another user).
            const sessionChanged =
              getStoredAuthSnapshot().refreshToken !== refreshToken ||
              getStoredAuthSnapshot().userRaw !== refreshSessionUser
            if (sessionChanged) {
              return Promise.reject({
                status: 401,
                code: 'AUTH_SESSION_CHANGED',
                message: 'Authentication session changed while refreshing.'
              })
            }

            const cleared = clearStoredAuthSessionIfMatches({
              accessToken: failedAccessToken ?? getStoredAuthToken(),
              refreshToken,
              userRaw: refreshSessionUser,
            })
            if (cleared) {
              publishAuthSessionEvent('invalidated', getStoredAuthSnapshot().userId, 'token_refresh_failed')
            }
            sessionStorage.setItem('auth_expired', '1')

            if (!window.location.pathname.includes('/login')) {
              window.location.href = '/login'
            }

            return Promise.reject({
              status: 401,
              code: 'TOKEN_REFRESH_FAILED',
              message: 'Session expired. Please log in again.'
            })
          }
        }

        // No refresh token or refresh endpoint: clear only the session that
        // produced this response, never a newer account's session.
        const currentSnapshot = getStoredAuthSnapshot()
        const headers = error.config?.headers as Record<string, unknown> | undefined
        const authHeader = headers?.Authorization ?? headers?.authorization
        const sentAuth =
          typeof authHeader === 'string'
            ? authHeader.trim() !== ''
            : Array.isArray(authHeader)
              ? authHeader.length > 0
              : !!authHeader
        const hasPersistedSession = Boolean(
          currentSnapshot.accessToken || currentSnapshot.refreshToken || currentSnapshot.userRaw,
        )

        const failedAccessToken =
          typeof authHeader === 'string' && authHeader.startsWith('Bearer ')
            ? authHeader.slice('Bearer '.length)
            : currentSnapshot.accessToken
        if (hasPersistedSession || sentAuth) {
          const cleared = clearStoredAuthSessionIfMatches({
            accessToken: failedAccessToken,
            userRaw: currentSnapshot.userRaw,
          })
          if (cleared) {
            publishAuthSessionEvent('invalidated', currentSnapshot.userId, isAuthRefresh ? 'refresh_unauthorized' : 'token_unauthorized')
          }
        }
        if ((hasPersistedSession || sentAuth) && !isAuthBootstrap) {
          sessionStorage.setItem('auth_expired', '1')
        }
        // Only redirect if not already on login page
        if (!window.location.pathname.includes('/login')) {
          window.location.href = '/login'
        }
      }

      // Return structured error
      return Promise.reject({
        status,
        code: apiData.code,
        reason: apiData.reason,
        error: apiData.error,
        message: apiData.message || apiData.detail || error.message,
        metadata: apiData.metadata,
      })
    }

    // Network error
    return Promise.reject({
      status: 0,
      message: 'Network error. Please check your connection.'
    })
  }
)

export default apiClient
