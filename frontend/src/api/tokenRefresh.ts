import axios from 'axios'
import type { ApiResponse } from '@/types'
import { getAPIBaseURL } from './url'
import {
  getStoredAuthToken,
  getStoredAuthUserId,
  getStoredRefreshToken,
  getStoredTokenExpiresAt,
  persistAccessToken,
  setStoredRefreshToken,
  setStoredTokenExpiresAt,
} from '@/utils/authStorage'
const TOKEN_REFRESH_LOCK_NAME = 'sub2api-auth-token-refresh'
const TOKEN_REFRESH_TIMEOUT_MS = 30_000
const TOKEN_REFRESH_BUFFER_MS = 120_000
const PEER_REFRESH_WAIT_MS = 1_000
const PEER_REFRESH_GRACE_MS = 1_000
const PEER_REFRESH_POLL_MS = 25

export interface RefreshTokenResponse {
  access_token: string
  refresh_token: string
  expires_in: number
  token_type: string
}

export interface RefreshAuthTokensOptions {
  /** Access token attached to the request that received a 401 response. */
  failedAccessToken?: string | null
}

interface AuthSnapshot {
  accessToken: string | null
  refreshToken: string
  expiresAt: number
  userID: number | null
}

const inFlightRefreshes = new Map<string, Promise<RefreshTokenResponse>>()

function getStoredUserID(): number | null {
  return getStoredAuthUserId()
}

function readAuthSnapshot(): AuthSnapshot {
  const refreshToken = getStoredRefreshToken()
  if (!refreshToken) {
    throw new Error('No refresh token available')
  }

  return {
    accessToken: getStoredAuthToken(),
    refreshToken,
    expiresAt: getStoredTokenExpiresAt() ?? 0,
    userID: getStoredUserID()
  }
}

function authSnapshotKey(snapshot: AuthSnapshot): string {
  return `${snapshot.userID ?? 'anonymous'}:${snapshot.refreshToken}`
}

function readStoredTokenPair(snapshot: AuthSnapshot): RefreshTokenResponse | null {
  const accessToken = getStoredAuthToken()
  const refreshToken = getStoredRefreshToken()
  const expiresAt = getStoredTokenExpiresAt() ?? 0

  if (
    !accessToken ||
    !refreshToken ||
    !Number.isFinite(expiresAt) ||
    expiresAt <= Date.now() ||
    getStoredUserID() !== snapshot.userID
  ) {
    return null
  }

  return {
    access_token: accessToken,
    refresh_token: refreshToken,
    expires_in: Math.max(1, Math.ceil((expiresAt - Date.now()) / 1000)),
    token_type: 'Bearer'
  }
}

function readPeerRefreshResult(
  snapshot: AuthSnapshot,
  failedAccessToken?: string | null
): RefreshTokenResponse | null {
  const storedPair = readStoredTokenPair(snapshot)
  if (!storedPair) {
    return null
  }

  if (storedPair.refresh_token !== snapshot.refreshToken) {
    return storedPair
  }

  if (
    failedAccessToken &&
    snapshot.accessToken !== failedAccessToken &&
    storedPair.access_token === snapshot.accessToken
  ) {
    return storedPair
  }

  if (!failedAccessToken) {
    const expiresAt = getStoredTokenExpiresAt() ?? 0
    if (
      expiresAt === snapshot.expiresAt &&
      storedPair.access_token === snapshot.accessToken &&
      expiresAt > Date.now() + TOKEN_REFRESH_BUFFER_MS
    ) {
      return storedPair
    }
  }

  return null
}

async function waitForPeerRefresh(
  snapshot: AuthSnapshot,
  failedAccessToken?: string | null,
  deadline = Date.now() + PEER_REFRESH_WAIT_MS
): Promise<RefreshTokenResponse | null> {
  while (Date.now() < deadline) {
    const peerResult = readPeerRefreshResult(snapshot, failedAccessToken)
    if (peerResult) {
      return peerResult
    }
    await new Promise((resolve) => window.setTimeout(resolve, PEER_REFRESH_POLL_MS))
  }

  return readPeerRefreshResult(snapshot, failedAccessToken)
}

function persistTokenPair(tokens: RefreshTokenResponse): void {
  persistAccessToken(tokens.access_token)
  setStoredTokenExpiresAt(tokens.expires_in)
  setStoredRefreshToken(tokens.refresh_token)
}

async function requestTokenPair(
  snapshot: AuthSnapshot,
  failedAccessToken?: string | null,
  mayHaveUncoordinatedPeer = false
): Promise<RefreshTokenResponse> {
  // If this request loses a one-time-token race, the winning peer can legitimately take as long
  // as our own HTTP timeout to publish its replacement token. Keep the recovery window tied to
  // that timeout instead of an arbitrary short delay.
  const peerRefreshDeadline = Date.now() + TOKEN_REFRESH_TIMEOUT_MS + PEER_REFRESH_GRACE_MS

  try {
    const response = await axios.post<ApiResponse<RefreshTokenResponse>>(
      `${getAPIBaseURL()}/auth/refresh`,
      { refresh_token: snapshot.refreshToken },
      { headers: { 'Content-Type': 'application/json' }, timeout: TOKEN_REFRESH_TIMEOUT_MS }
    )
    const payload = response.data
    if (payload.code !== 0 || !payload.data) {
      throw new Error(payload.message || 'Token refresh failed')
    }

    if (
      getStoredRefreshToken() !== snapshot.refreshToken ||
      getStoredUserID() !== snapshot.userID
    ) {
      const peerResult = readPeerRefreshResult(snapshot, failedAccessToken)
      if (peerResult) {
        return peerResult
      }
      throw new Error('Session changed during token refresh')
    }

    persistTokenPair(payload.data)
    return payload.data
  } catch (error) {
    // A peer tab may have rotated the one-time refresh token while this request was in flight.
    // A 4xx response can arrive quickly while the winning peer's response is still in flight, so
    // wait through the shared request deadline before treating the session as expired. Transient
    // non-4xx failures retain the short reconciliation window.
    const responseStatus = (error as { response?: { status?: unknown } }).response?.status
    const isTokenRejection =
      typeof responseStatus === 'number' && responseStatus >= 400 && responseStatus < 500
    const peerResult = await waitForPeerRefresh(
      snapshot,
      failedAccessToken,
      isTokenRejection && mayHaveUncoordinatedPeer
        ? peerRefreshDeadline
        : Date.now() + PEER_REFRESH_WAIT_MS
    )
    if (peerResult) {
      return peerResult
    }
    throw error
  }
}

async function runRefresh(
  options: RefreshAuthTokensOptions,
  snapshot: AuthSnapshot
): Promise<RefreshTokenResponse> {
  const refresh = async (mayHaveUncoordinatedPeer = false): Promise<RefreshTokenResponse> => {
    const peerResult = readPeerRefreshResult(snapshot, options.failedAccessToken)
    if (peerResult) {
      return peerResult
    }
    return requestTokenPair(snapshot, options.failedAccessToken, mayHaveUncoordinatedPeer)
  }

  if (typeof navigator !== 'undefined' && navigator.locks) {
    return navigator.locks.request(TOKEN_REFRESH_LOCK_NAME, () => refresh(false))
  }

  return refresh(true)
}

/**
 * Refresh and persist the browser session.
 *
 * Calls in the same document share one promise per session. Web Locks serialize refreshes across
 * tabs, while the token snapshot check adopts a peer's newly rotated token instead of logging the
 * user out.
 */
export function refreshAuthTokens(
  options: RefreshAuthTokensOptions = {}
): Promise<RefreshTokenResponse> {
  const snapshot = readAuthSnapshot()
  const key = authSnapshotKey(snapshot)
  const existingRefresh = inFlightRefreshes.get(key)
  if (existingRefresh) {
    return existingRefresh
  }

  const pending = runRefresh(options, snapshot)
  inFlightRefreshes.set(key, pending)
  const clearPending = (): void => {
    if (inFlightRefreshes.get(key) === pending) {
      inFlightRefreshes.delete(key)
    }
  }
  void pending.then(clearPending, clearPending)
  return pending
}
