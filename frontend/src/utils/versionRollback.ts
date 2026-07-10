export const DEFAULT_UPDATE_REPOSITORY = 'DoodleXu/sub2api'

export function normalizeUpdateRepository(repository?: string): string {
  const value = (repository || '').trim()
  if (!value) return DEFAULT_UPDATE_REPOSITORY

  const normalized = value
    .replace(/^https:\/\/github\.com\//i, '')
    .replace(/\.git$/i, '')
    .replace(/^\/+|\/+$/g, '')

  return normalized.includes('/') ? normalized : DEFAULT_UPDATE_REPOSITORY
}

export function buildScriptRollbackCommand(repository: string | undefined, version: string): string {
  const targetVersion = version.trim().replace(/^v/i, '')
  if (!targetVersion) return ''

  const tag = `v${targetVersion}`
  return `curl -sSL https://raw.githubusercontent.com/${normalizeUpdateRepository(repository)}/${tag}/deploy/install.sh | sudo bash -s -- rollback ${tag}`
}

export function buildGhcrImageName(repository: string | undefined): string {
  return `ghcr.io/${normalizeUpdateRepository(repository).toLowerCase()}`
}
