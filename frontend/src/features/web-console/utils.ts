function endpointPath(base: string): string {
  const value = base.trim()
  if (!value) return ''
  try {
    return new URL(value, globalThis.location?.origin || 'http://localhost').pathname.replace(/\/+$/, '').toLowerCase()
  } catch {
    return value.replace(/^https?:\/\/[^/]+/i, '').replace(/\/+$/, '').toLowerCase()
  }
}

export function isWebConsoleOpenAICompatibleEndpoint(base: string): boolean {
  const path = endpointPath(base)
  return !(
    path.endsWith('/v1beta') ||
    path.includes('/v1beta/') ||
    path.endsWith('/antigravity/v1') ||
    path.includes('/antigravity/v1/') ||
    path.endsWith('/antigravity/v1beta') ||
    path.includes('/antigravity/v1beta/')
  )
}

export function webConsoleErrorMessage(error: unknown): string {
  if (error instanceof Error && error.message) {
    if (/quota (has been )?exceeded|quota exceeded|insufficient_quota/i.test(error.message)) {
      return '当前额度已用尽，请切换 API Key 或稍后再试。'
    }
    return error.message
  }
  return '请求失败，请稍后重试。'
}
