export const AUTH_TOKEN_KEY = 'auth_token'
export const AUTH_USER_KEY = 'auth_user'
export const REFRESH_TOKEN_KEY = 'refresh_token'
export const TOKEN_EXPIRES_AT_KEY = 'token_expires_at'
export const PENDING_OAUTH_REFRESH_TOKEN_KEY = 'sub2api_pending_oauth_refresh_token'
export const PENDING_OAUTH_TOKEN_EXPIRES_AT_KEY = 'sub2api_pending_oauth_token_expires_at'

export interface AuthStorageUser {
  id?: unknown
  email?: unknown
  [key: string]: unknown
}

export interface AuthStorageSnapshot {
  accessToken: string | null
  refreshToken: string | null
  userRaw: string | null
  userId: number | null
}

export interface AuthStorageMatch {
  accessToken?: string | null
  refreshToken?: string | null
  userRaw?: string | null
  userId?: number | null
}

function getStorage(): Storage | null {
  return typeof localStorage === 'undefined' ? null : localStorage
}

function getSessionStorage(): Storage | null {
  return typeof sessionStorage === 'undefined' ? null : sessionStorage
}

function parseUser(raw: string | null): AuthStorageUser | null {
  if (!raw) return null

  try {
    const parsed = JSON.parse(raw) as unknown
    return parsed && typeof parsed === 'object' ? parsed as AuthStorageUser : null
  } catch {
    return null
  }
}

function normalizeUserID(value: unknown): number | null {
  const id = Number(value)
  return Number.isFinite(id) && id > 0 ? id : null
}

export function getStoredAuthUserId(): number | null {
  const storage = getStorage()
  return normalizeUserID(parseUser(storage?.getItem(AUTH_USER_KEY) ?? null)?.id)
}

export function getStoredAuthSnapshot(): AuthStorageSnapshot {
  const storage = getStorage()
  const userRaw = storage?.getItem(AUTH_USER_KEY) ?? null

  return {
    accessToken: storage?.getItem(AUTH_TOKEN_KEY) ?? null,
    refreshToken: storage?.getItem(REFRESH_TOKEN_KEY) ?? null,
    userRaw,
    userId: normalizeUserID(parseUser(userRaw)?.id),
  }
}

export function persistAuthSession(options: {
  accessToken: string
  refreshToken?: string | null
  expiresIn?: number | null
  expiresAt?: number | null
  user: object
}): void {
  const storage = getStorage()
  if (!storage) return

  storage.setItem(AUTH_TOKEN_KEY, options.accessToken)

  if (options.refreshToken !== undefined) {
    if (options.refreshToken) {
      storage.setItem(REFRESH_TOKEN_KEY, options.refreshToken)
    } else {
      storage.removeItem(REFRESH_TOKEN_KEY)
    }
  }

  if (options.expiresAt !== undefined || options.expiresIn !== undefined) {
    if (options.expiresAt && Number.isFinite(options.expiresAt) && options.expiresAt > 0) {
      storage.setItem(TOKEN_EXPIRES_AT_KEY, String(options.expiresAt))
    } else if (options.expiresIn && options.expiresIn > 0) {
      storage.setItem(TOKEN_EXPIRES_AT_KEY, String(Date.now() + options.expiresIn * 1000))
    } else {
      storage.removeItem(TOKEN_EXPIRES_AT_KEY)
    }
  }

  // Publish the identity last. Cross-tab listeners use the dedicated auth event
  // as the commit marker, so intermediate token fields are never treated as a
  // complete session.
  storage.setItem(AUTH_USER_KEY, JSON.stringify(options.user))
}

export function persistAccessToken(accessToken: string): void {
  getStorage()?.setItem(AUTH_TOKEN_KEY, accessToken)
}

export function setStoredRefreshToken(refreshToken: string): void {
  getStorage()?.setItem(REFRESH_TOKEN_KEY, refreshToken)
}

export function setStoredTokenExpiresAt(expiresIn: number): void {
  getStorage()?.setItem(TOKEN_EXPIRES_AT_KEY, String(Date.now() + expiresIn * 1000))
}

export interface PendingOAuthTokenContext {
  refreshToken: string | null
  tokenExpiresAt: number | null
}

export function persistPendingOAuthTokenContext(tokens: {
  refreshToken?: string | null
  expiresIn?: number | null
}): void {
  const storage = getSessionStorage()
  if (!storage) return

  storage.removeItem(PENDING_OAUTH_REFRESH_TOKEN_KEY)
  storage.removeItem(PENDING_OAUTH_TOKEN_EXPIRES_AT_KEY)

  if (tokens.refreshToken) {
    storage.setItem(PENDING_OAUTH_REFRESH_TOKEN_KEY, tokens.refreshToken)
  }
  if (tokens.expiresIn && tokens.expiresIn > 0) {
    storage.setItem(
      PENDING_OAUTH_TOKEN_EXPIRES_AT_KEY,
      String(Date.now() + tokens.expiresIn * 1000),
    )
  }
}

export function getPendingOAuthTokenContext(): PendingOAuthTokenContext {
  const storage = getSessionStorage()
  const rawExpiresAt = storage?.getItem(PENDING_OAUTH_TOKEN_EXPIRES_AT_KEY) ?? null
  const parsedExpiresAt = rawExpiresAt ? Number.parseInt(rawExpiresAt, 10) : NaN

  return {
    refreshToken: storage?.getItem(PENDING_OAUTH_REFRESH_TOKEN_KEY) ?? null,
    tokenExpiresAt: Number.isFinite(parsedExpiresAt) ? parsedExpiresAt : null,
  }
}

export function clearPendingOAuthTokenContext(): void {
  const storage = getSessionStorage()
  if (!storage) return

  storage.removeItem(PENDING_OAUTH_REFRESH_TOKEN_KEY)
  storage.removeItem(PENDING_OAUTH_TOKEN_EXPIRES_AT_KEY)
}

export function getStoredAuthToken(): string | null {
  return getStorage()?.getItem(AUTH_TOKEN_KEY) ?? null
}

export function getStoredRefreshToken(): string | null {
  return getStorage()?.getItem(REFRESH_TOKEN_KEY) ?? null
}

export function getStoredTokenExpiresAt(): number | null {
  const raw = getStorage()?.getItem(TOKEN_EXPIRES_AT_KEY)
  if (!raw) return null

  const expiresAt = Number.parseInt(raw, 10)
  return Number.isFinite(expiresAt) ? expiresAt : null
}

export function clearStoredAuthSession(): void {
  const storage = getStorage()
  if (!storage) return

  storage.removeItem(AUTH_TOKEN_KEY)
  storage.removeItem(AUTH_USER_KEY)
  storage.removeItem(REFRESH_TOKEN_KEY)
  storage.removeItem(TOKEN_EXPIRES_AT_KEY)
}

export function clearStoredAuthSessionIfMatches(match: AuthStorageMatch): boolean {
  const current = getStoredAuthSnapshot()
  const hasExpectedValue = Object.values(match).some((value) => value !== undefined)

  if (
    hasExpectedValue &&
    (match.accessToken !== undefined && current.accessToken !== match.accessToken ||
      match.refreshToken !== undefined && current.refreshToken !== match.refreshToken ||
      match.userRaw !== undefined && current.userRaw !== match.userRaw ||
      match.userId !== undefined && current.userId !== match.userId)
  ) {
    return false
  }

  clearStoredAuthSession()
  return true
}
