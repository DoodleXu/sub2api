export const AUTH_SESSION_EVENT_NAME = 'sub2api-auth-session-event'
export const AUTH_SESSION_STORAGE_KEY = 'sub2api-auth-session-event'
export const AUTH_SESSION_CHANNEL_NAME = 'sub2api-auth-session'

export type AuthSessionEventType =
  | 'authenticated'
  | 'identity_changed'
  | 'logged_out'
  | 'invalidated'
  | 'role_mismatch'

const AUTH_SESSION_EVENT_TYPES = new Set<AuthSessionEventType>([
  'authenticated',
  'identity_changed',
  'logged_out',
  'invalidated',
  'role_mismatch',
])

export interface AuthSessionEventPayload {
  eventId: string
  sourceId: string
  type: AuthSessionEventType
  userId: number | null
  reason?: string
  createdAt: number
}

export interface ReceivedAuthSessionEvent extends AuthSessionEventPayload {
  source: 'local' | 'cross-tab'
}

type AuthSessionEventHandler = (event: ReceivedAuthSessionEvent) => void

const sourceId = createSourceID()

function createSourceID(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return `${Date.now()}-${Math.random().toString(36).slice(2)}`
}

function parseEvent(value: unknown): AuthSessionEventPayload | null {
  if (!value || typeof value !== 'object') return null

  const candidate = value as Partial<AuthSessionEventPayload>
  if (
    typeof candidate.eventId !== 'string' ||
    typeof candidate.sourceId !== 'string' ||
    typeof candidate.type !== 'string' ||
    typeof candidate.createdAt !== 'number' ||
    !AUTH_SESSION_EVENT_TYPES.has(candidate.type as AuthSessionEventType)
  ) {
    return null
  }

  return {
    eventId: candidate.eventId,
    sourceId: candidate.sourceId,
    type: candidate.type as AuthSessionEventType,
    userId: typeof candidate.userId === 'number' ? candidate.userId : null,
    reason: typeof candidate.reason === 'string' ? candidate.reason : undefined,
    createdAt: candidate.createdAt,
  }
}

function createEvent(type: AuthSessionEventType, userId: number | null, reason?: string): AuthSessionEventPayload {
  return {
    eventId: `${sourceId}:${Date.now()}:${Math.random().toString(36).slice(2)}`,
    sourceId,
    type,
    userId,
    reason,
    createdAt: Date.now(),
  }
}

export function publishAuthSessionEvent(
  type: AuthSessionEventType,
  userId: number | null,
  reason?: string,
): void {
  if (typeof window === 'undefined') return

  const payload = createEvent(type, userId, reason)
  window.dispatchEvent(new CustomEvent<AuthSessionEventPayload>(AUTH_SESSION_EVENT_NAME, {
    detail: payload,
  }))

  try {
    localStorage.setItem(AUTH_SESSION_STORAGE_KEY, JSON.stringify(payload))
  } catch {
    // Storage can be unavailable in privacy mode; the local event still works.
  }

  if (typeof BroadcastChannel !== 'undefined') {
    try {
      const channel = new BroadcastChannel(AUTH_SESSION_CHANNEL_NAME)
      channel.postMessage(payload)
      channel.close()
    } catch {
      // BroadcastChannel is an optimization; storage events remain the fallback.
    }
  }
}

export function subscribeToAuthSessionEvents(handler: AuthSessionEventHandler): () => void {
  if (typeof window === 'undefined') return () => undefined

  const seenEventIDs = new Set<string>()
  const deliver = (payload: AuthSessionEventPayload | null, source: 'local' | 'cross-tab'): void => {
    if (!payload || seenEventIDs.has(payload.eventId)) return
    seenEventIDs.add(payload.eventId)
    if (seenEventIDs.size > 32) {
      const first = seenEventIDs.values().next().value
      if (typeof first === 'string') seenEventIDs.delete(first)
    }
    handler({ ...payload, source })
  }

  const onLocalEvent = (event: Event): void => {
    const detail = (event as CustomEvent<AuthSessionEventPayload>).detail
    deliver(parseEvent(detail), 'local')
  }
  const onStorageEvent = (event: StorageEvent): void => {
    if (event.key !== AUTH_SESSION_STORAGE_KEY || !event.newValue) return
    try {
      deliver(parseEvent(JSON.parse(event.newValue)), 'cross-tab')
    } catch {
      // Ignore malformed storage events from stale clients.
    }
  }

  window.addEventListener(AUTH_SESSION_EVENT_NAME, onLocalEvent)
  window.addEventListener('storage', onStorageEvent)

  let channel: BroadcastChannel | null = null
  const onChannelMessage = (event: MessageEvent<unknown>): void => {
    deliver(parseEvent(event.data), 'cross-tab')
  }
  if (typeof BroadcastChannel !== 'undefined') {
    try {
      channel = new BroadcastChannel(AUTH_SESSION_CHANNEL_NAME)
      channel.addEventListener('message', onChannelMessage)
    } catch {
      channel = null
    }
  }

  return () => {
    window.removeEventListener(AUTH_SESSION_EVENT_NAME, onLocalEvent)
    window.removeEventListener('storage', onStorageEvent)
    channel?.removeEventListener('message', onChannelMessage)
    channel?.close()
  }
}
