import type { WebConsoleMode, WebConsoleSession } from './types'

const STORAGE_KEY = 'sub2api-web-console-sessions-v1'
const MAX_SESSIONS = 50

function nowIso(): string {
  return new Date().toISOString()
}

function newId(prefix: string): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) {
    return `${prefix}-${crypto.randomUUID()}`
  }
  return `${prefix}-${Date.now()}-${Math.random().toString(16).slice(2)}`
}

export function createWebConsoleSession(mode: WebConsoleMode): WebConsoleSession {
  const now = nowIso()
  return {
    id: newId('session'),
    title: mode === 'image' ? '新生图会话' : '新对话',
    mode,
    messages: [],
    created_at: now,
    updated_at: now,
  }
}

export function createWebConsoleMessageId(): string {
  return newId('message')
}

export function loadWebConsoleSessions(): WebConsoleSession[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return []
    const parsed = JSON.parse(raw) as WebConsoleSession[]
    if (!Array.isArray(parsed)) return []
    return parsed
      .filter((item) => typeof item?.id === 'string' && Array.isArray(item.messages))
      .slice(0, MAX_SESSIONS)
  } catch {
    return []
  }
}

export function saveWebConsoleSessions(sessions: WebConsoleSession[]): void {
  const normalized = sessions
    .map((session) => ({
      ...session,
      updated_at: session.updated_at || nowIso(),
    }))
    .sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at))
    .slice(0, MAX_SESSIONS)
  localStorage.setItem(STORAGE_KEY, JSON.stringify(normalized))
}

export function titleFromPrompt(prompt: string, fallback: string): string {
  const compact = prompt.trim().replace(/\s+/g, ' ')
  if (!compact) return fallback
  return compact.length > 24 ? `${compact.slice(0, 24)}...` : compact
}
