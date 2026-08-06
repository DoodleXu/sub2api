import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  AUTH_SESSION_EVENT_NAME,
  AUTH_SESSION_STORAGE_KEY,
  publishAuthSessionEvent,
  subscribeToAuthSessionEvents,
} from '@/utils/authSessionEvents'

describe('auth session events', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    localStorage.clear()
  })

  it('delivers local session events without exposing token data', () => {
    const handler = vi.fn()
    const stop = subscribeToAuthSessionEvents(handler)

    publishAuthSessionEvent('authenticated', 931)

    expect(handler).toHaveBeenCalledWith(expect.objectContaining({
      source: 'local',
      type: 'authenticated',
      userId: 931,
    }))
    expect(JSON.parse(localStorage.getItem(AUTH_SESSION_STORAGE_KEY) || '{}')).not.toHaveProperty('token')
    stop()
  })

  it('delivers a storage event from another tab', () => {
    const handler = vi.fn()
    const stop = subscribeToAuthSessionEvents(handler)

    window.dispatchEvent(new StorageEvent('storage', {
      key: AUTH_SESSION_STORAGE_KEY,
      newValue: JSON.stringify({
        eventId: 'other-tab-event',
        sourceId: 'other-tab',
        type: 'identity_changed',
        userId: 931,
        createdAt: Date.now(),
      }),
    }))

    expect(handler).toHaveBeenCalledWith(expect.objectContaining({
      source: 'cross-tab',
      type: 'identity_changed',
      userId: 931,
    }))
    stop()
  })

  it('supports local listeners through the stable event name', () => {
    const listener = vi.fn()
    window.addEventListener(AUTH_SESSION_EVENT_NAME, listener)

    publishAuthSessionEvent('logged_out', null, 'user_logout')

    expect(listener).toHaveBeenCalledTimes(1)
    expect((listener.mock.calls[0][0] as CustomEvent).detail).toMatchObject({
      type: 'logged_out',
      userId: null,
    })
    window.removeEventListener(AUTH_SESSION_EVENT_NAME, listener)
  })
})
