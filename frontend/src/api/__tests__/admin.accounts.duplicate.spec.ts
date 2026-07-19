import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({
  post: vi.fn()
}))

vi.mock('@/api/client', () => ({
  apiClient: { post }
}))

import { duplicate } from '@/api/admin/accounts'

describe('admin account duplicate API', () => {
  beforeEach(() => {
    sessionStorage.clear()
    localStorage.clear()
    localStorage.setItem('auth_user', JSON.stringify({ id: 7 }))
    post.mockReset()
    post.mockResolvedValue({ data: { id: 43, name: 'primary (Copy)' } })
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue('11111111-1111-4111-8111-111111111111')
  })

  it('sends a stable idempotency key with the duplicate request', async () => {
    const account = await duplicate(42)

    expect(post).toHaveBeenCalledWith('/admin/accounts/42/duplicate', undefined, {
      headers: {
        'Idempotency-Key': 'account-duplicate-7-42-11111111-1111-4111-8111-111111111111'
      }
    })
    expect(account).toEqual({ id: 43, name: 'primary (Copy)' })
  })

  it('reuses the operation key after an ambiguous failed request', async () => {
    post.mockRejectedValueOnce(new Error('network timeout'))
    await expect(duplicate(99)).rejects.toThrow('network timeout')

    post.mockResolvedValueOnce({ data: { id: 100, name: 'retry (Copy)' } })
    await duplicate(99)

    expect(post).toHaveBeenCalledTimes(2)
    const firstHeaders = post.mock.calls[0][2].headers
    const secondHeaders = post.mock.calls[1][2].headers
    expect(secondHeaders).toEqual(firstHeaders)
  })

  it('reuses the operation key after a page reload', async () => {
    post.mockRejectedValueOnce(new Error('network timeout'))
    await expect(duplicate(77)).rejects.toThrow('network timeout')
    const firstHeaders = post.mock.calls[0][2].headers

    vi.resetModules()
    post.mockResolvedValueOnce({ data: { id: 78, name: 'reload (Copy)' } })
    const { duplicate: duplicateAfterReload } = await import('@/api/admin/accounts')
    await duplicateAfterReload(77)

    expect(post).toHaveBeenCalledTimes(2)
    expect(post.mock.calls[1][2].headers).toEqual(firstHeaders)
    expect(sessionStorage.length).toBe(0)
  })

  it('isolates pending operation keys between administrators', async () => {
    post.mockRejectedValueOnce(new Error('admin A timeout'))
    await expect(duplicate(42)).rejects.toThrow('admin A timeout')
    const adminAKey = post.mock.calls[0][2].headers['Idempotency-Key']

    localStorage.setItem('auth_user', JSON.stringify({ id: 8 }))
    post.mockResolvedValueOnce({ data: { id: 44, name: 'admin B copy' } })
    await duplicate(42)
    const adminBKey = post.mock.calls[1][2].headers['Idempotency-Key']

    expect(adminBKey).not.toBe(adminAKey)
    expect(adminBKey).toContain('account-duplicate-8-42-')

    localStorage.setItem('auth_user', JSON.stringify({ id: 7 }))
    post.mockResolvedValueOnce({ data: { id: 43, name: 'admin A recovered copy' } })
    await duplicate(42)
    expect(post.mock.calls[2][2].headers['Idempotency-Key']).toBe(adminAKey)
  })
})
